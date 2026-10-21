// Package eval measures retrieval quality against a labelled dataset.
// Port of docgraph/evaluation (dataset format, metrics and table layout are identical).
//
// Relevance is judged at two levels:
//   - document: the result's document is one of expected_documents
//   - section:  additionally its heading_path equals one of expected_sections or
//     is nested below one ("A > B > C" is inside "A > B"); cases without
//     expected_sections fall back to document relevance.
//
// Recall@k is the hit rate (1 if any relevant result is in the top k).
// MRR = mean of 1/rank of the first relevant result (0 if none returned).
package eval

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/skromesch/docgraph/internal/model"
	"github.com/skromesch/docgraph/internal/store"
)

var KS = []int{1, 3, 5, 10}

const Depth = 10

type Case struct {
	ID                string   `json:"id"`
	Query             string   `json:"query"`
	ExpectedDocuments []string `json:"expected_documents"`
	ExpectedSections  []string `json:"expected_sections"`
	Tags              []string `json:"tags"`
}

// LoadDataset reads a JSON array or JSONL file (lines starting with // are comments).
func LoadDataset(path string) ([]Case, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cases []Case
	if strings.HasSuffix(path, ".jsonl") {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "//") {
				continue
			}
			var c Case
			if err := json.Unmarshal([]byte(line), &c); err != nil {
				return nil, err
			}
			cases = append(cases, c)
		}
	} else if err := json.Unmarshal(data, &cases); err != nil {
		return nil, err
	}
	for i := range cases {
		if cases[i].ID == "" {
			cases[i].ID = fmt.Sprintf("q%03d", i+1)
		}
		if len(cases[i].ExpectedDocuments) == 0 {
			return nil, fmt.Errorf("%s: expected_documents is empty", cases[i].ID)
		}
	}
	return cases, nil
}

func inSection(hp string, sections []string) bool {
	for _, s := range sections {
		if hp == s || strings.HasPrefix(hp, s+" > ") {
			return true
		}
	}
	return false
}

// Validate reports expected documents/sections that do not exist in the index.
func Validate(cases []Case, db *store.DB) ([]string, error) {
	docs, err := db.Documents()
	if err != nil {
		return nil, err
	}
	rows, err := db.Conn.Query("SELECT d.path, c.heading_path FROM chunks c JOIN documents d ON d.id = c.document_id")
	if err != nil {
		return nil, err
	}
	paths := map[string][]string{}
	for rows.Next() {
		var p, hp string
		rows.Scan(&p, &hp)
		paths[p] = append(paths[p], hp)
	}
	rows.Close()
	var problems []string
	for _, c := range cases {
		for _, d := range c.ExpectedDocuments {
			if _, ok := docs[d]; !ok {
				problems = append(problems, fmt.Sprintf("%s: document not indexed: %s", c.ID, d))
			}
		}
		for _, s := range c.ExpectedSections {
			found := false
			for _, d := range c.ExpectedDocuments {
				for _, hp := range paths[d] {
					if inSection(hp, []string{s}) {
						found = true
					}
				}
			}
			if !found {
				problems = append(problems, fmt.Sprintf("%s: section not found: %s", c.ID, s))
			}
		}
	}
	return problems, nil
}

func IsRelevant(r model.SearchResult, c Case, level string) bool {
	if !slices.Contains(c.ExpectedDocuments, r.DocumentPath) {
		return false
	}
	if level == "document" || len(c.ExpectedSections) == 0 {
		return true
	}
	return inSection(r.HeadingPath, c.ExpectedSections)
}

// FirstRelevantRank is 1-based; 0 = not found.
func FirstRelevantRank(results []model.SearchResult, c Case, level string) int {
	for i, r := range results {
		if IsRelevant(r, c, level) {
			return i + 1
		}
	}
	return 0
}

type Scores struct {
	N     int
	Hits  map[int]int
	RRSum float64
}

func newScores() Scores { return Scores{Hits: map[int]int{1: 0, 3: 0, 5: 0, 10: 0}} }

func (s *Scores) Add(rank int) {
	s.N++
	if rank == 0 {
		return
	}
	s.RRSum += 1 / float64(rank)
	for _, k := range KS {
		if rank <= k {
			s.Hits[k]++
		}
	}
}

func (s Scores) Recall(k int) float64 {
	if s.N == 0 {
		return 0
	}
	return float64(s.Hits[k]) / float64(s.N)
}

func (s Scores) MRR() float64 {
	if s.N == 0 {
		return 0
	}
	return s.RRSum / float64(s.N)
}

type QueryResult struct {
	ID           string   `json:"id"`
	Query        string   `json:"query"`
	Tags         []string `json:"tags"`
	SectionRank  *int     `json:"section_rank"`
	DocumentRank *int     `json:"document_rank"`
	Top          []string `json:"top"`
}

type ModeResult struct {
	Mode      string
	Section   Scores
	Document  Scores
	LatencyMS float64
	PerQuery  []QueryResult
}

// SearchFunc runs one query at Depth.
type SearchFunc func(query string, k int) ([]model.SearchResult, error)

func optRank(r int) *int {
	if r == 0 {
		return nil
	}
	return &r
}

func Evaluate(mode string, search SearchFunc, cases []Case) (ModeResult, error) {
	mr := ModeResult{Mode: mode, Section: newScores(), Document: newScores()}
	t0 := time.Now()
	for _, c := range cases {
		hits, err := search(c.Query, Depth)
		if err != nil {
			return mr, fmt.Errorf("%s: %w", c.ID, err)
		}
		sec := FirstRelevantRank(hits, c, "section")
		doc := FirstRelevantRank(hits, c, "document")
		mr.Section.Add(sec)
		mr.Document.Add(doc)
		var top []string
		for _, h := range hits[:min(3, len(hits))] {
			top = append(top, h.DocumentPath+" :: "+h.HeadingPath)
		}
		mr.PerQuery = append(mr.PerQuery, QueryResult{c.ID, c.Query, c.Tags, optRank(sec), optRank(doc), top})
	}
	if len(cases) > 0 {
		mr.LatencyMS = float64(time.Since(t0).Microseconds()) / 1000 / float64(len(cases))
	}
	return mr, nil
}

type Row struct {
	Label  string
	Result ModeResult
}

func FormatTable(rows []Row, level string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-34s", "")
	for _, k := range KS {
		fmt.Fprintf(&b, "%8s", fmt.Sprintf("R@%d", k))
	}
	fmt.Fprintf(&b, "%8s%8s", "MRR", "ms/q")
	for _, r := range rows {
		s := r.Result.Section
		if level == "document" {
			s = r.Result.Document
		}
		label := []rune(r.Label)
		if len(label) > 34 {
			label = label[:34]
		}
		fmt.Fprintf(&b, "\n%-34s", string(label))
		for _, k := range KS {
			fmt.Fprintf(&b, "%8.3f", s.Recall(k))
		}
		fmt.Fprintf(&b, "%8.3f%8.1f", s.MRR(), r.Result.LatencyMS)
	}
	return b.String()
}

func rankStr(r *int) string {
	if r == nil {
		return "None"
	}
	return fmt.Sprint(*r)
}

func FormatFailures(mr ModeResult, limit int) string {
	var out []string
	for _, q := range mr.PerQuery {
		if q.SectionRank == nil || *q.SectionRank > 3 {
			out = append(out, fmt.Sprintf("  [%s] rank=%s doc_rank=%s  %s", q.ID, rankStr(q.SectionRank), rankStr(q.DocumentRank), q.Query))
			for _, t := range q.Top {
				out = append(out, "        "+t)
			}
		}
	}
	if len(out) == 0 {
		return "  (none)"
	}
	return strings.Join(out[:min(len(out), limit*4)], "\n")
}

func round4(x float64) float64 { return float64(int64(x*10000+0.5)) / 10000 }

func (s Scores) asMap() map[string]any {
	m := map[string]any{"n": s.N, "MRR": round4(s.MRR())}
	for _, k := range KS {
		m[fmt.Sprintf("R@%d", k)] = round4(s.Recall(k))
	}
	return m
}

// ToJSON mirrors runner.to_json.
func ToJSON(rows []Row) ([]byte, error) {
	var out []map[string]any
	for _, r := range rows {
		out = append(out, map[string]any{"variant": r.Label, "mode": r.Result.Mode,
			"section": r.Result.Section.asMap(), "document": r.Result.Document.asMap(),
			"latency_ms": r.Result.LatencyMS, "per_query": r.Result.PerQuery})
	}
	if out == nil {
		return nil, errors.New("no results")
	}
	return json.MarshalIndent(out, "", "  ")
}
