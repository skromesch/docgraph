package search

import (
	"testing"

	"github.com/skromesch/docgraph/internal/model"
	"github.com/skromesch/docgraph/internal/store"
)

// Same cases as tests/test_service_mcp.py::test_confidence_labels.
func TestConfidence(t *testing.T) {
	d := func(top int64, bm []int64, vec ...store.Hit) *Detail {
		hits := make([]store.Hit, len(bm))
		for i, r := range bm {
			hits[i] = store.Hit{Rowid: r}
		}
		return &Detail{Results: []model.SearchResult{{}}, Rowids: []int64{top}, BM25Hits: hits, VectorHits: vec}
	}
	for i, c := range []struct {
		d    *Detail
		want string
	}{
		{d(1, []int64{1}, store.Hit{1, 0.90}), "high"},
		{d(1, []int64{2, 3, 4, 5, 6, 1}, store.Hit{1, 0.90}), "medium"},
		{d(1, []int64{1}, store.Hit{1, 0.85}), "medium"},
		{d(1, []int64{1}, store.Hit{2, 0.90}), "low"},
		{d(1, []int64{1}, store.Hit{1, 0.82}), "low"},
		{&Detail{}, "low"},
	} {
		if got := Confidence(c.d, 30); got != c.want {
			t.Errorf("case %d: %s, want %s", i, got, c.want)
		}
	}
}
