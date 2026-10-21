package search

import (
	"testing"

	"github.com/skromesch/docgraph/internal/config"
	"github.com/skromesch/docgraph/internal/store"
	"github.com/skromesch/docgraph/internal/testutil"
	"github.com/skromesch/docgraph/internal/testutil/fixture"
)

// Queries over the fixture wiki (testdata/fixtures/wiki); go test -update
// rewrites the goldens.
var queries = []string{
	"closing an inventory",
	"Starting an inventory permissions",
	"select locations from the hierarchy",
	"walkthrough master state",
	"mobile synchronization offline",
	"push transactions",
	"inventory states",
	"API endpoint token",
	"Missing status",
	"inventor",
	"role",
	"zzzqqq no such thing",
	"a",
	"C# .NET \"quoted\" (parens)",
	"café",
}

var bm25Variants = map[string]config.BM25{
	"default": {HeadingWeight: 5, HeadingPathWeight: 3, ContentWeight: 1, MinTermLen: 1},
	"prefix5": {HeadingWeight: 5, HeadingPathWeight: 3, ContentWeight: 1, MinTermLen: 1, PrefixLen: 5},
	"min3":    {HeadingWeight: 5, HeadingPathWeight: 3, ContentWeight: 1, MinTermLen: 3},
	"weights": {HeadingWeight: 1, HeadingPathWeight: 1, ContentWeight: 2, MinTermLen: 1},
}

type hit struct {
	Rowid int64   `json:"rowid"`
	Score float64 `json:"score"`
}

func hits(hs []store.Hit) []hit {
	out := make([]hit, len(hs))
	for i, h := range hs {
		out[i] = hit{h.Rowid, testutil.Round(h.Score, 6)}
	}
	return out
}

func TestBM25Golden(t *testing.T) {
	db, _ := fixture.Index(t, nil)
	type variant struct {
		Match string `json:"match"`
		Hits  []hit  `json:"hits"`
	}
	out := map[string]map[string]variant{}
	for _, q := range queries {
		out[q] = map[string]variant{}
		for name, cfg := range bm25Variants {
			hs, err := (&BM25{db, cfg}).SearchRowids(q, 30)
			if err != nil {
				t.Fatalf("%q: %v", q, err)
			}
			out[q][name] = variant{BuildMatchQuery(q, cfg.PrefixLen, cfg.MinTermLen), hits(hs)}
		}
	}
	testutil.GoldenJSON(t, "fixtures-bm25.json", out)
}

// Vector, fused and hybrid rankings with the real model (skipped when not pulled).
func TestHybridGolden(t *testing.T) {
	e := fixture.Embedder(t)
	db, _ := fixture.Index(t, e)
	vec, err := NewVector(db, e, store.SqliteVec{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default().Search
	h := &Hybrid{&BM25{db, cfg.BM25}, vec, cfg}
	type result struct {
		ChunkID string         `json:"chunk_id"`
		Score   float64        `json:"score"`
		Ranks   map[string]int `json:"ranks"`
	}
	type entry struct {
		Vector     []hit    `json:"vector"`
		Hybrid     []result `json:"hybrid"`
		Confidence string   `json:"confidence"`
	}
	out := map[string]entry{}
	for _, q := range queries {
		d, err := h.SearchDetail(q, 10)
		if err != nil {
			t.Fatal(err)
		}
		var en entry
		for _, r := range d.Results {
			en.Hybrid = append(en.Hybrid, result{r.ChunkID, testutil.Round(r.Score, 6), r.Ranks})
		}
		// scores rounded: ONNX results differ in the last bits across CPUs
		for _, v := range hits(d.VectorHits) {
			en.Vector = append(en.Vector, hit{v.Rowid, testutil.Round(v.Score, 3)})
		}
		en.Confidence = Confidence(d, cfg.VectorCandidates)
		out[q] = en
	}
	testutil.GoldenJSON(t, "fixtures-hybrid.json", out)
}

func TestRRF(t *testing.T) {
	got := RRF([]Ranking{
		{"bm25", []int64{1, 2, 3, 2}, 1}, // duplicate 2 keeps rank 2
		{"vector", []int64{3, 4, 1}, 2},
	}, 60)
	want := []Fused{
		{1, 1.0/61 + 2.0/63, map[string]int{"bm25": 1, "vector": 3}},
		{3, 1.0/63 + 2.0/61, map[string]int{"bm25": 3, "vector": 1}},
		{4, 2.0 / 62, map[string]int{"bm25": 0, "vector": 2}},
		{2, 1.0 / 62, map[string]int{"bm25": 2, "vector": 0}},
	}
	// 3 (0.04866) > 1 (0.04814) > 4 (0.03226) > 2 (0.01613)
	want[0], want[1] = want[1], want[0]
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.Item != w.Item || testutil.Round(g.Score, 12) != testutil.Round(w.Score, 12) ||
			g.Ranks["bm25"] != w.Ranks["bm25"] || g.Ranks["vector"] != w.Ranks["vector"] {
			t.Errorf("pos %d: got %+v want %+v", i, g, w)
		}
	}
	// equal scores: better best rank first, then first appearance
	tie := RRF([]Ranking{{"a", []int64{7, 8}, 1}, {"b", []int64{8, 7}, 1}}, 60)
	if tie[0].Item != 7 || tie[1].Item != 8 {
		t.Errorf("tie order %+v", tie)
	}
}
