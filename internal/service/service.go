// Package service is the long-lived query service behind the MCP server.
// Outputs are compact plain text: the consumer is an LLM agent and every
// output token is paid for.
package service

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/text/unicode/norm"

	"github.com/skromesch/docgraph/internal/chunk"
	"github.com/skromesch/docgraph/internal/config"
	"github.com/skromesch/docgraph/internal/embed"
	"github.com/skromesch/docgraph/internal/index"
	"github.com/skromesch/docgraph/internal/model"
	"github.com/skromesch/docgraph/internal/pyutil"
	"github.com/skromesch/docgraph/internal/search"
	"github.com/skromesch/docgraph/internal/store"
)

// ConfidenceText is the first line of hybrid search output.
var ConfidenceText = map[string]string{
	"high":   "Confidence: high (keyword and semantic search agree on the top hit).",
	"medium": "Confidence: medium.",
	"low": "Confidence: low. The hits may be off-topic: rephrase, try the other language, " +
		"or grep for a word stem before concluding it is undocumented.",
}

const (
	MaxTopK      = 20
	MaxReadLines = 400
	MaxSiblings  = 8
	ModelWait    = 90 * time.Second
	SyncInterval = 5 * time.Second // auto-sync at most this often (a check is a stat() per file)
)

type Service struct {
	DB         *store.DB
	Cfg        config.Config
	SourceRoot string

	mu       sync.Mutex // one SQLite connection + one model: serialize calls
	bm25     *search.BM25
	vector   *search.Vector
	hybrid   *search.Hybrid
	embedder embed.Embedder
	ready    chan struct{}

	autoSync   bool
	lastSync   time.Time
	LastSync   *index.Summary
	counter    chunk.Counter
	syncPeriod time.Duration
}

func New(db *store.DB, cfg config.Config, sourceRoot string) *Service {
	if sourceRoot == "" {
		sourceRoot, _ = db.GetMeta("source_root")
	}
	s := &Service{DB: db, Cfg: cfg, SourceRoot: sourceRoot, bm25: &search.BM25{DB: db, Cfg: cfg.Search.BM25},
		ready: make(chan struct{}), syncPeriod: SyncInterval}
	return s
}

// SetEmbedder installs the model (nil = keyword only) and marks the service ready.
func (s *Service) SetEmbedder(e embed.Embedder) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setEmbedderLocked(e)
}

func (s *Service) setEmbedderLocked(e embed.Embedder) error {
	defer s.markReady()
	if e == nil {
		return nil
	}
	v, err := search.NewVector(s.DB, e, store.SqliteVec{DB: s.DB})
	if err != nil {
		return err
	}
	s.embedder, s.vector = e, v
	s.hybrid = &search.Hybrid{BM25: s.bm25, Vector: v, Cfg: s.Cfg.Search}
	return nil
}

func (s *Service) markReady() {
	select {
	case <-s.ready:
	default:
		close(s.ready)
	}
}

// LoadModelInBackground starts serving at once and loads the model in a
// goroutine: MCP clients give up on servers that are slow to start. Tool calls
// that arrive earlier wait for it (keyword search does not).
func (s *Service) LoadModelInBackground(load func() (embed.Embedder, error)) {
	go func() {
		e, err := load()
		s.mu.Lock()
		defer s.mu.Unlock()
		if err != nil {
			fmt.Fprintf(os.Stderr, "docgraph: model not loaded, keyword search only: %v\n", err)
			e = nil
		}
		if err := s.setEmbedderLocked(e); err != nil {
			fmt.Fprintf(os.Stderr, "docgraph: %v\n", err)
		}
	}()
}

func (s *Service) waitReady() bool {
	select {
	case <-s.ready:
		return true
	case <-time.After(ModelWait):
		return false
	}
}

// -- sync ---------------------------------------------------------------------

// EnableAutoSync: before a tool call, re-index files changed on disk (at most every SyncInterval).
func (s *Service) EnableAutoSync(counter chunk.Counter) {
	s.autoSync, s.counter = true, counter
}

// Sync runs one incremental pass over the source root. Caller holds the lock.
func (s *Service) syncLocked() (*index.Summary, error) {
	if st, err := os.Stat(s.SourceRoot); s.SourceRoot == "" || err != nil || !st.IsDir() {
		return nil, nil
	}
	if s.counter == nil {
		s.counter = chunk.GetCounter(s.Cfg.Embedding.Model)
	}
	sum, err := index.New(s.DB, s.Cfg, s.embedder, s.counter).Index(s.SourceRoot, nil, true)
	s.lastSync, s.LastSync = time.Now(), &sum
	return &sum, err
}

func (s *Service) maybeSync() {
	if s.autoSync && time.Since(s.lastSync) >= s.syncPeriod {
		if _, err := s.syncLocked(); err != nil { // a broken file must not break search
			s.lastSync = time.Now()
		}
	}
}

// -- tools ----------------------------------------------------------------------

// Search: mode "" = hybrid when the model is loaded, else keyword.
func (s *Service) Search(query, mode string, topK int) string {
	if mode != "keyword" && !s.waitReady() {
		mode = "keyword" // model still loading (or stuck): answer with BM25 rather than hang
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maybeSync()
	return s.search(query, mode, topK)
}

func (s *Service) ReadDocument(p string, startLine, endLine int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maybeSync()
	return s.readDocument(p, startLine, endLine)
}

func (s *Service) ListDocuments(glob string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maybeSync()
	return s.listDocuments(glob)
}

func (s *Service) search(query, mode string, topK int) string {
	topK = max(1, min(topK, MaxTopK))
	if mode == "" || mode == "default" {
		mode = "hybrid"
	}
	if mode != "keyword" && s.vector == nil {
		mode = "keyword"
	}
	var results []model.SearchResult
	var detail *search.Detail
	var err error
	switch mode {
	case "keyword":
		results, err = s.bm25.Search(query, topK)
	case "semantic":
		results, err = s.vector.Search(query, topK)
	case "hybrid":
		if detail, err = s.hybrid.SearchDetail(query, topK); err == nil {
			results = detail.Results
		}
	default:
		return fmt.Sprintf("Unknown mode '%s'. Use hybrid, keyword or semantic.", mode)
	}
	if err != nil {
		return "Search failed: " + err.Error()
	}
	if len(results) == 0 && mode != "hybrid" {
		return "No results."
	}
	shown := map[[2]string]bool{}
	for _, r := range results {
		shown[[2]string{r.DocumentPath, r.HeadingPath}] = true
	}
	seen := map[string]bool{}
	blocks := make([]string, 0, len(results))
	for _, r := range results {
		block := fmt.Sprintf("[%d] %s:%d-%d\nSection: %s\n\n%s", r.Rank, r.DocumentPath, r.StartLine, r.EndLine,
			r.HeadingPath, strings.TrimSpace(r.Content))
		if !seen[r.DocumentPath] {
			seen[r.DocumentPath] = true
			if others := s.otherSections(r.DocumentPath, shown); others != "" {
				block += "\n\n(Other sections in this file: " + others + ")"
			}
		}
		blocks = append(blocks, block)
	}
	if mode != "hybrid" {
		return strings.Join(blocks, "\n\n---\n\n")
	}
	c := s.Cfg.Search
	head := ConfidenceText[search.Confidence(detail, c.VectorCandidates)]
	diag, err := search.Diagrams(s.bm25, query, detail, c.BM25Candidates, c.RRF.K, seen)
	if err != nil {
		return "Search failed: " + err.Error()
	}
	if len(diag) > 0 {
		lines := make([]string, len(diag))
		for i, d := range diag {
			lines[i] = fmt.Sprintf("[D%d] %s:%d-%d | %s", i+1, d.Path, d.StartLine, d.EndLine, d.Title)
		}
		blocks = append(blocks, "Related diagrams (PlantUML/Mermaid source, read with read_document):\n"+strings.Join(lines, "\n"))
	}
	if len(blocks) == 0 {
		return "No results."
	}
	return head + "\n\n" + strings.Join(blocks, "\n\n---\n\n")
}

// otherSections lists sibling sections not in the results, so the agent knows
// what it has not seen (e.g. the Backup step next to a retrieved Restore step).
func (s *Service) otherSections(docPath string, shown map[[2]string]bool) string {
	rows, err := s.DB.Conn.Query("SELECT c.heading_path, MIN(c.start_line), MAX(c.end_line) FROM chunks c "+
		"JOIN documents d ON d.id = c.document_id WHERE d.path = ? "+
		"GROUP BY c.heading_path ORDER BY MIN(c.start_line)", docPath)
	if err != nil {
		return ""
	}
	defer rows.Close()
	var items []string
	for rows.Next() {
		var hp string
		var a, b int
		rows.Scan(&hp, &a, &b)
		if shown[[2]string{docPath, hp}] {
			continue
		}
		parts := strings.Split(hp, " > ")
		items = append(items, fmt.Sprintf("%s (%d-%d)", parts[len(parts)-1], a, b))
	}
	if len(items) == 0 {
		return ""
	}
	more := ""
	if len(items) > MaxSiblings {
		more = fmt.Sprintf(", +%d more", len(items)-MaxSiblings)
		items = items[:MaxSiblings]
	}
	return strings.Join(items, "; ") + more
}

func (s *Service) readDocument(p string, startLine, endLine int) string {
	p = norm.NFC.String(strings.TrimLeft(strings.TrimSpace(p), "/"))
	docs, err := s.DB.Documents()
	if err != nil {
		return "Read failed: " + err.Error()
	}
	if _, ok := docs[p]; !ok {
		return fmt.Sprintf("Not an indexed document: %s. Use list_documents or search to find paths.", p)
	}
	full := s.fileFor(p)
	if full == "" {
		return "File not found on disk: " + p
	}
	raw, err := os.ReadFile(full)
	if err != nil {
		return "Read failed: " + err.Error()
	}
	lines := pyutil.SplitLines(pyutil.DecodeFile(raw))
	start := max(1, startLine)
	end := len(lines)
	if endLine > 0 {
		end = min(len(lines), endLine)
	}
	truncated := false
	if end-start+1 > MaxReadLines {
		end, truncated = start+MaxReadLines-1, true
	}
	body := ""
	if start <= end && start-1 < len(lines) {
		body = strings.Join(lines[start-1:end], "\n")
	}
	out := fmt.Sprintf("%s (lines %d-%d of %d)\n\n%s", p, start, end, len(lines), body)
	if truncated {
		out += fmt.Sprintf("\n\n[truncated; continue with start_line=%d]", end+1)
	}
	return out
}

func (s *Service) listDocuments(glob string) string {
	pattern := norm.NFC.String(glob)
	if pattern == "" {
		pattern = "*"
	}
	rows, err := s.DB.Conn.Query("SELECT d.path, d.title, COUNT(c.rowid) FROM documents d " +
		"LEFT JOIN chunks c ON c.document_id = d.id GROUP BY d.id ORDER BY d.path")
	if err != nil {
		return "List failed: " + err.Error()
	}
	defer rows.Close()
	var hits []string
	for rows.Next() {
		var p, title string
		var n int
		rows.Scan(&p, &title, &n)
		if fnmatch(pattern, p) || fnmatch(pattern+"/*", p) {
			hits = append(hits, fmt.Sprintf("%s | %s | %d sections", p, title, n))
		}
	}
	if len(hits) == 0 {
		return fmt.Sprintf("No documents match '%s'.", glob)
	}
	more := ""
	if len(hits) > 300 {
		more = fmt.Sprintf("\n... %d more", len(hits)-300)
		hits = hits[:300]
	}
	return strings.Join(hits, "\n") + more
}

// fnmatch mirrors Python's fnmatch.fnmatch on POSIX: '*' also matches '/'.
func fnmatch(pattern, name string) bool {
	// path.Match's '*' stops at '/', so translate '*' to a sequence of segment wildcards
	ok, err := path.Match(pattern, name)
	if err == nil && ok {
		return true
	}
	return globAcrossSlashes(pattern, name)
}

func globAcrossSlashes(p, s string) bool {
	if p == "" {
		return s == ""
	}
	switch p[0] {
	case '*':
		for i := 0; i <= len(s); i++ {
			if globAcrossSlashes(p[1:], s[i:]) {
				return true
			}
		}
		return false
	case '?':
		if s == "" {
			return false
		}
		_, size := firstRune(s)
		return globAcrossSlashes(p[1:], s[size:])
	case '[':
		if end := strings.IndexByte(p, ']'); end > 0 && s != "" {
			r, size := firstRune(s)
			ok, _ := path.Match(p[:end+1], string(r))
			return ok && globAcrossSlashes(p[end+1:], s[size:])
		}
	}
	if s == "" || p[0] != s[0] {
		return false
	}
	return globAcrossSlashes(p[1:], s[1:])
}

func firstRune(s string) (rune, int) {
	for i, r := range s {
		if i == 0 {
			return r, len(string(r))
		}
	}
	return 0, 0
}

func (s *Service) fileFor(p string) string {
	if s.SourceRoot == "" {
		return ""
	}
	for _, form := range []norm.Form{norm.NFC, norm.NFD} {
		c := filepath.Join(s.SourceRoot, filepath.FromSlash(form.String(p)))
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return ""
}
