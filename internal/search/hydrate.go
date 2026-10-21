// Package search implements keyword (BM25), semantic and hybrid (RRF) retrieval.
// Port of docgraph/search; see docs/RETRIEVAL.md.
package search

import (
	"github.com/skromesch/docgraph/internal/model"
	"github.com/skromesch/docgraph/internal/store"
)

// Hydrate turns scored rowids into results, keeping their order; rank is 1-based
// and counts hits that no longer exist.
func Hydrate(db *store.DB, hits []store.Hit, source string) ([]model.SearchResult, error) {
	rowids := make([]int64, len(hits))
	for i, h := range hits {
		rowids[i] = h.Rowid
	}
	rows, err := db.ChunksByRowid(rowids)
	if err != nil {
		return nil, err
	}
	out := make([]model.SearchResult, 0, len(hits))
	for i, h := range hits {
		r, ok := rows[h.Rowid]
		if !ok {
			continue
		}
		out = append(out, model.SearchResult{
			ChunkID: r.ID, DocumentPath: r.DocumentPath, HeadingPath: r.HeadingPath, Content: r.Content,
			StartLine: r.StartLine, EndLine: r.EndLine, Score: h.Score, Rank: i + 1,
			Ranks: map[string]int{source: i + 1},
		})
	}
	return out, nil
}
