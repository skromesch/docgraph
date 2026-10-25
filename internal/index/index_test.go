package index

import (
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/skromesch/docgraph/internal/chunk"
	"github.com/skromesch/docgraph/internal/config"
	"github.com/skromesch/docgraph/internal/embed"
	"github.com/skromesch/docgraph/internal/store"
	"github.com/skromesch/docgraph/internal/testutil"
)

// -- helpers ------------------------------------------------------------------

func copyDir(t *testing.T, src string) string {
	dst := filepath.Join(t.TempDir(), "wiki")
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

// fakeEmbedder: deterministic hashed bag of characters, counts model calls.
type fakeEmbedder struct{ calls, texts int }

func (f *fakeEmbedder) Name() string               { return "fake" }
func (f *fakeEmbedder) Dim() int                   { return 16 }
func (f *fakeEmbedder) Prefix(k embed.Kind) string { return string(k) + ": " }
func (f *fakeEmbedder) Close() error               { return nil }
func (f *fakeEmbedder) Embed(texts []string, k embed.Kind) ([][]float32, error) {
	f.calls++
	f.texts += len(texts)
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, 16)
		sum := sha256.Sum256([]byte(string(k) + t))
		for j := range v {
			v[j] = float32(binary.LittleEndian.Uint16(sum[2*j:])) - 32768
		}
		embed.Normalize(v)
		out[i] = v
	}
	return out, nil
}

func newDB(t *testing.T) *store.DB {
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func count(t *testing.T, db *store.DB, q string) int {
	var n int
	if err := db.Conn.QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// -- scan parity ----------------------------------------------------------------

func TestScanGolden(t *testing.T) {
	data, err := os.ReadFile(testutil.GoldenPath("scan-cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name   string   `json:"name"`
		Config []string `json:"config"`
		File   *string  `json:"file"`
		Files  []string `json:"files"`
	}
	json.Unmarshal(data, &cases)
	cfg := config.Default()
	for _, c := range cases {
		root := copyDir(t, "../../testdata/fixtures/wiki")
		if c.File != nil {
			os.WriteFile(filepath.Join(root, IgnoreFile), []byte(*c.File), 0o644)
		}
		files, err := Scan(root, cfg.Index.Extensions, cfg.Index.ExcludeDirs, c.Config)
		if err != nil {
			t.Fatal(err)
		}
		got := make([]string, 0, len(files))
		for p := range files {
			got = append(got, p)
		}
		sort.Strings(got)
		if !reflect.DeepEqual(got, c.Files) {
			t.Errorf("%s:\n got  %v\n want %v", c.Name, got, c.Files)
		}
	}
}

// Agent instruction files are skipped by default at any depth; a negated
// pattern in .docgraphignore brings a file back.
func TestAgentFilesIgnoredByDefault(t *testing.T) {
	root := copyDir(t, "../../testdata/fixtures/wiki")
	for _, f := range []string{"CLAUDE.md", "Processes/AGENTS.md", ".claude/skills/x.md", "sub/.cursor/rules.md",
		".github/copilot-instructions.md", ".github/prompts/p.md", ".github/README.md", "GEMINI.md"} {
		os.MkdirAll(filepath.Dir(filepath.Join(root, f)), 0o755)
		os.WriteFile(filepath.Join(root, f), []byte("# x\n"), 0o644)
	}
	cfg := config.Default()
	scan := func() []string {
		files, err := Scan(root, cfg.Index.Extensions, cfg.Index.ExcludeDirs, cfg.Index.Ignore)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for p := range files {
			if !strings.HasPrefix(p, "Docs/") && p != "Inventory.md" && p != "notes.txt" && p != "Processes/Café-walkthrough.md" {
				got = append(got, p)
			}
		}
		sort.Strings(got)
		return got
	}
	if got := scan(); !reflect.DeepEqual(got, []string{".github/README.md"}) {
		t.Fatalf("default: %v", got)
	}
	os.WriteFile(filepath.Join(root, IgnoreFile), []byte("!CLAUDE.md\n"), 0o644)
	if got := scan(); !reflect.DeepEqual(got, []string{".github/README.md", "CLAUDE.md"}) {
		t.Fatalf("re-included: %v", got)
	}
}

// -- database parity ------------------------------------------------------------

func dump(t *testing.T, conn *sql.DB, q string) []string {
	rows, err := conn.Query(q)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var out []string
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		rows.Scan(ptrs...)
		for i, v := range vals {
			if b, ok := v.([]byte); ok {
				vals[i] = string(b)
			}
		}
		out = append(out, fmt.Sprint(vals...))
	}
	return out
}

// Indexing the fixture wiki without embeddings: every stored row is compared
// with testdata/golden/fixtures-index.txt.
func TestIndexGolden(t *testing.T) {
	db := newDB(t)
	if _, err := New(db, config.Default(), nil, chunk.Heuristic).Index(testutil.Fixtures(), nil, false); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, q := range []string{
		"SELECT id, path, title, type, content_hash, size_bytes FROM documents ORDER BY path",
		"SELECT rowid, id, document_id, parent_chunk_id, heading, heading_path, heading_level, content, embedding_text, " +
			"content_hash, embedding_key, start_line, end_line, chunk_index, part, token_count FROM chunks ORDER BY rowid",
		"SELECT rowid FROM chunks_fts WHERE chunks_fts MATCH 'inventory OR assets OR synchronization' ORDER BY rank, rowid",
		"SELECT key, value FROM metadata WHERE key = 'schema_version'",
	} {
		out = append(out, "-- "+q)
		out = append(out, dump(t, db.Conn, q)...)
	}
	testutil.Golden(t, "fixtures-index.txt", []byte(strings.Join(out, "\n")+"\n"))
}

// -- incremental behaviour ------------------------------------------------------

func TestIncrementalLifecycle(t *testing.T) {
	root := copyDir(t, "../../testdata/fixtures/wiki")
	db := newDB(t)
	emb := &fakeEmbedder{}
	ix := New(db, config.Default(), emb, chunk.Heuristic)

	s, err := ix.Index(root, nil, false)
	if err != nil || s.New != 6 || s.ChunksEmbedded == 0 || s.Failed != 0 {
		t.Fatalf("first run %+v %v", s, err)
	}
	chunks := count(t, db, "SELECT COUNT(*) FROM chunks")
	if count(t, db, "SELECT COUNT(*) FROM chunk_embeddings") != chunks {
		t.Fatal("not every chunk has a vector")
	}

	// unchanged: nothing embedded, quick check reads nothing
	calls := emb.calls
	s, _ = ix.Index(root, nil, true)
	if s.Unchanged != 6 || s.Changed() || emb.calls != calls {
		t.Fatalf("second run %+v", s)
	}

	// modify one section: only new text hits the model, the rest is reused
	p := filepath.Join(root, "Inventory.md")
	data, _ := os.ReadFile(p)
	os.WriteFile(p, append(data, []byte("\n\n## New section\n\nAbout the quarkcloud.\n")...), 0o644)
	texts := emb.texts
	s, _ = ix.Index(root, nil, true)
	if s.Modified != 1 || s.ChunksEmbedded != 1 || s.EmbeddingsReused == 0 || emb.texts-texts != 1 {
		t.Fatalf("modify run %+v (embedded texts %d)", s, emb.texts-texts)
	}
	if count(t, db, "SELECT COUNT(*) FROM chunks_fts WHERE chunks_fts MATCH 'quarkcloud'") != 1 {
		t.Fatal("FTS not updated")
	}

	// touch without change: re-hashed, unchanged
	future := float64(1e9)
	os.Chtimes(p, timeOf(future), timeOf(future))
	s, _ = ix.Index(root, nil, true)
	if s.Modified != 0 || s.Unchanged != 6 {
		t.Fatalf("touch run %+v", s)
	}

	// ignore + delete: documents, chunks, FTS rows and vectors all go
	os.WriteFile(filepath.Join(root, IgnoreFile), []byte("notes.txt\n"), 0o644)
	if err := os.Remove(filepath.Join(root, "Docs", "sync.mmd")); err != nil {
		t.Fatal(err)
	}
	s, _ = ix.Index(root, nil, true)
	if s.Deleted != 2 {
		t.Fatalf("delete run %+v", s)
	}
	n := count(t, db, "SELECT COUNT(*) FROM chunks")
	if count(t, db, "SELECT COUNT(*) FROM chunk_embeddings") != n ||
		count(t, db, "SELECT COUNT(*) FROM chunk_embeddings WHERE rowid NOT IN (SELECT rowid FROM chunks)") != 0 {
		t.Fatal("orphaned vectors")
	}
	if count(t, db, "SELECT COUNT(*) FROM embedding_cache") != n {
		t.Fatal("embedding cache not pruned")
	}
}

// A run without an embedder must still delete vectors (regression).
func TestDeleteWithoutEmbedderLeavesNoOrphans(t *testing.T) {
	root := copyDir(t, "../../testdata/fixtures/wiki")
	db := newDB(t)
	New(db, config.Default(), &fakeEmbedder{}, chunk.Heuristic).Index(root, nil, false)
	os.Remove(filepath.Join(root, "notes.txt"))
	s, err := New(db, config.Default(), nil, chunk.Heuristic).Index(root, nil, true)
	if err != nil || s.Deleted != 1 {
		t.Fatalf("%+v %v", s, err)
	}
	if count(t, db, "SELECT COUNT(*) FROM chunk_embeddings WHERE rowid NOT IN (SELECT rowid FROM chunks)") != 0 {
		t.Fatal("orphaned vectors")
	}
	// pending chunks are embedded by a later run with an embedder
	os.WriteFile(filepath.Join(root, "uj.md"), []byte("# New\n\ntext\n"), 0o644)
	New(db, config.Default(), nil, chunk.Heuristic).Index(root, nil, true)
	if count(t, db, "SELECT COUNT(*) FROM chunks WHERE embedding_key IS NULL") == 0 {
		t.Fatal("expected pending chunks")
	}
	s, _ = New(db, config.Default(), &fakeEmbedder{}, chunk.Heuristic).Index(root, nil, true)
	if s.ChunksEmbedded != 1 || count(t, db, "SELECT COUNT(*) FROM chunks WHERE embedding_key IS NULL") != 0 {
		t.Fatalf("backlog not embedded: %+v", s)
	}
}

func TestMtimeFloatSeconds(t *testing.T) {
	// st_mtime = sec + nsec * 1e-9 as a double
	st, _ := os.Stat("index_test.go")
	m := Mtime(st)
	if math.Abs(m-float64(st.ModTime().UnixNano())/1e9) > 1e-6 {
		t.Fatalf("mtime %f", m)
	}
}

func timeOf(sec float64) (tm time.Time) { return time.Unix(int64(sec), 0) }

func TestNoEmbedPassKeepsCacheForPendingChunks(t *testing.T) {
	root := copyDir(t, "../../testdata/fixtures/wiki")
	db := newDB(t)
	New(db, config.Default(), &fakeEmbedder{}, chunk.Heuristic).Index(root, nil, false)
	p := filepath.Join(root, "Inventory.md")
	data, _ := os.ReadFile(p)
	os.WriteFile(p, append(data, []byte("\n\n## New\n\nquarkcloud\n")...), 0o644)
	New(db, config.Default(), nil, chunk.Heuristic).Index(root, nil, true) // sync pass 1
	s, _ := New(db, config.Default(), &fakeEmbedder{}, chunk.Heuristic).Index(root, nil, true)
	if s.ChunksEmbedded != 1 || s.EmbeddingsReused == 0 {
		t.Fatalf("expected reuse: %+v", s)
	}
}
