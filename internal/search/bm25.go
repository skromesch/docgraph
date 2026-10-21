package search

import (
	"regexp"
	"slices"
	"strings"

	"github.com/skromesch/docgraph/internal/config"
	"github.com/skromesch/docgraph/internal/model"
	"github.com/skromesch/docgraph/internal/pyutil"
	"github.com/skromesch/docgraph/internal/store"
)

var wordRE = regexp.MustCompile(`[` + pyutil.Word + `]+`)

// BuildMatchQuery turns free text into an FTS5 MATCH expression: Unicode word
// terms, lowercased, de-duplicated, each quoted (FTS5 operators in the input are
// never interpreted), optionally truncated to prefix queries, joined with OR.
func BuildMatchQuery(query string, prefixLen, minTermLen int) string {
	var terms []string
	for _, word := range wordRE.FindAllString(pyutil.Lower(query), -1) {
		runes := []rune(word)
		if len(runes) < minTermLen {
			continue
		}
		term := `"` + word + `"`
		if prefixLen > 0 && len(runes) > prefixLen {
			term = `"` + string(runes[:prefixLen]) + `"*`
		}
		if !slices.Contains(terms, term) {
			terms = append(terms, term)
		}
	}
	return strings.Join(terms, " OR ")
}

type BM25 struct {
	DB  *store.DB
	Cfg config.BM25
}

// SearchRowids returns hits best first; the score is -bm25() (higher is better).
// With docTypes, only chunks of documents of those types are searched.
func (b *BM25) SearchRowids(query string, k int, docTypes ...string) ([]store.Hit, error) {
	match := BuildMatchQuery(query, b.Cfg.PrefixLen, b.Cfg.MinTermLen)
	if match == "" {
		return nil, nil
	}
	args := []any{b.Cfg.HeadingWeight, b.Cfg.HeadingPathWeight, b.Cfg.ContentWeight, match}
	q := "SELECT rowid, bm25(chunks_fts, ?, ?, ?) AS s FROM chunks_fts WHERE chunks_fts MATCH ? ORDER BY s LIMIT ?"
	if len(docTypes) > 0 {
		q = "SELECT f.rowid, bm25(chunks_fts, ?, ?, ?) AS s FROM chunks_fts f " +
			"JOIN chunks ch ON ch.rowid = f.rowid JOIN documents d ON d.id = ch.document_id " +
			"WHERE chunks_fts MATCH ? AND d.type IN (" + strings.TrimSuffix(strings.Repeat("?,", len(docTypes)), ",") +
			") ORDER BY s LIMIT ?"
		for _, t := range docTypes {
			args = append(args, t)
		}
	}
	rows, err := b.DB.Conn.Query(q, append(args, k)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Hit
	for rows.Next() {
		var h store.Hit
		if err := rows.Scan(&h.Rowid, &h.Score); err != nil {
			return nil, err
		}
		h.Score = -h.Score
		out = append(out, h)
	}
	return out, rows.Err()
}

func (b *BM25) Search(query string, k int) ([]model.SearchResult, error) {
	hits, err := b.SearchRowids(query, k)
	if err != nil {
		return nil, err
	}
	return Hydrate(b.DB, hits, "bm25")
}
