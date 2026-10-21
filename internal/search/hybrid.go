package search

import (
	"sort"

	"github.com/skromesch/docgraph/internal/config"
	"github.com/skromesch/docgraph/internal/model"
	"github.com/skromesch/docgraph/internal/store"
)

// Ranking is one retriever's result list (rowids, best first).
type Ranking struct {
	Name   string
	Items  []int64
	Weight float64
}

type Fused struct {
	Item  int64
	Score float64
	Ranks map[string]int // 1-based; 0 = not retrieved
}

// RRF fuses ranked lists: score(d) = sum_r w_r / (k + rank_r(d)).
// Only ranks are used, never raw scores. Tie-break: higher score, then better
// best-rank, then first appearance when scanning the lists in the given order.
// A duplicate within one list keeps its best (first) rank.
func RRF(rankings []Ranking, k int) []Fused {
	scores := map[int64]float64{}
	ranks := map[int64]map[string]int{}
	firstSeen := map[int64]int{}
	var order []int64
	for _, r := range rankings {
		for i, item := range r.Items {
			rank := i + 1
			if rk, ok := ranks[item]; ok && rk[r.Name] != 0 {
				continue
			}
			if _, ok := firstSeen[item]; !ok {
				firstSeen[item] = len(firstSeen)
				order = append(order, item)
			}
			scores[item] += r.Weight / float64(k+rank)
			if ranks[item] == nil {
				ranks[item] = map[string]int{}
				for _, rr := range rankings {
					ranks[item][rr.Name] = 0
				}
			}
			ranks[item][r.Name] = rank
		}
	}
	bestRank := func(item int64) int {
		best := 0
		for _, v := range ranks[item] {
			if v != 0 && (best == 0 || v < best) {
				best = v
			}
		}
		return best
	}
	sort.SliceStable(order, func(a, b int) bool {
		x, y := order[a], order[b]
		if scores[x] != scores[y] {
			return scores[x] > scores[y]
		}
		if bx, by := bestRank(x), bestRank(y); bx != by {
			return bx < by
		}
		return firstSeen[x] < firstSeen[y]
	})
	out := make([]Fused, len(order))
	for i, item := range order {
		out[i] = Fused{item, scores[item], ranks[item]}
	}
	return out
}

// VectorPool is how many vector hits a query fetches: the first VectorCandidates
// are fused, the rest feed the diagram block (diagram chunks are a small share).
const VectorPool = 200

// Detail is a hybrid search plus the retriever lists behind it, for the
// confidence label and the diagram block.
type Detail struct {
	Results    []model.SearchResult
	Rowids     []int64     // chunk rowid of each result
	BM25Hits   []store.Hit // -bm25, best first
	VectorHits []store.Hit // cosine, best first, VectorPool deep
}

type Hybrid struct {
	BM25   *BM25
	Vector *Vector
	Cfg    config.Search
}

func (h *Hybrid) Search(query string, k int) ([]model.SearchResult, error) {
	d, err := h.SearchDetail(query, k)
	if err != nil {
		return nil, err
	}
	return d.Results, nil
}

func (h *Hybrid) SearchDetail(query string, k int) (*Detail, error) {
	c := h.Cfg
	if k <= 0 {
		k = c.TopK
	}
	bm, err := h.BM25.SearchRowids(query, c.BM25Candidates)
	if err != nil {
		return nil, err
	}
	pool, err := h.Vector.SearchRowids(query, max(VectorPool, c.VectorCandidates))
	if err != nil {
		return nil, err
	}
	vec := pool[:min(len(pool), c.VectorCandidates)]
	fused := RRF([]Ranking{
		{"bm25", rowids(bm), c.RRF.BM25Weight},
		{"vector", rowids(vec), c.RRF.VectorWeight},
	}, c.RRF.K)
	if len(fused) > k {
		fused = fused[:k]
	}
	hits := make([]store.Hit, len(fused))
	for i, f := range fused {
		hits[i] = store.Hit{Rowid: f.Item, Score: f.Score}
	}
	results, err := Hydrate(h.BM25.DB, hits, "hybrid")
	if err != nil {
		return nil, err
	}
	// re-attach per-retriever ranks (Hydrate skips rows that vanished)
	byRowid := map[string]map[string]int{}
	rows, err := h.BM25.DB.ChunksByRowid(rowids(hits))
	if err != nil {
		return nil, err
	}
	for _, f := range fused {
		if r, ok := rows[f.Item]; ok {
			byRowid[r.ID] = f.Ranks
		}
	}
	ids := make([]int64, 0, len(results))
	for _, f := range fused {
		if _, ok := rows[f.Item]; ok {
			ids = append(ids, f.Item)
		}
	}
	for i := range results {
		results[i].Ranks = byRowid[results[i].ChunkID]
	}
	return &Detail{Results: results, Rowids: ids, BM25Hits: bm, VectorHits: pool}, nil
}

func rowids(hits []store.Hit) []int64 {
	out := make([]int64, len(hits))
	for i, h := range hits {
		out[i] = h.Rowid
	}
	return out
}
