package search

// Answer-quality signals added to hybrid search output (see docs/RETRIEVAL.md).
//
// Confidence label. RRF scores are rank-based, so the best hit always scores
// well, even when it is off-topic. The label uses two absolute signals of the
// top hit instead: its cosine similarity to the query (e5 cosines sit in a
// narrow band, about 0.75-0.90), and whether BM25 and the vector search both
// rank it in their top AgreeTop.
//
//	low     the top hit is not among the vector hits, or the best cosine < LowCos
//	high    the best cosine >= HighCos and both retrievers rank the top hit top 5
//	medium  otherwise
//
// The thresholds were calibrated for multilingual-e5-base on a Hungarian wiki
// (60 questions, 8 of them unanswerable): every "high" hit was right, and 7 of
// the 8 unanswerable questions were labelled "low". Re-calibrate them for
// another model.
//
// Diagram block. Text chunks crowd diagram chunks (PlantUML/Mermaid sources)
// out of the fused list, so diagrams get their own RRF over diagram-only BM25
// and vector lists. A diagram is listed when its best chunk is in both lists
// and its cosine is >= DiagramMinCos; at most MaxDiagrams files, compactly
// (path, line range, title), skipping files already in the text results.

import "github.com/skromesch/docgraph/internal/store"

const (
	HighCos       = 0.865
	LowCos        = 0.83
	AgreeTop      = 5
	DiagramMinCos = 0.80
	MaxDiagrams   = 3
)

var DiagramTypes = []string{"plantuml", "mermaid"}

// Confidence labels the top hit "high", "medium" or "low".
func Confidence(d *Detail, vectorCandidates int) string {
	if len(d.Results) == 0 || len(d.Rowids) == 0 {
		return "low"
	}
	vec := d.VectorHits[:min(len(d.VectorHits), vectorCandidates)]
	top := d.Rowids[0]
	best := 0.0
	if len(vec) > 0 {
		best = vec[0].Score
	}
	inVec := indexOf(vec, top)
	if inVec < 0 || best < LowCos {
		return "low"
	}
	if best >= HighCos && inVec < AgreeTop && indexOf(d.BM25Hits[:min(len(d.BM25Hits), AgreeTop)], top) >= 0 {
		return "high"
	}
	return "medium"
}

func indexOf(hits []store.Hit, rowid int64) int {
	for i, h := range hits {
		if h.Rowid == rowid {
			return i
		}
	}
	return -1
}

type DiagramHit struct {
	Path, Title        string
	StartLine, EndLine int
}

// Diagrams fuses diagram-only BM25 and vector lists and returns up to
// MaxDiagrams files not in shown, whose best chunk is in both lists with a
// cosine of at least DiagramMinCos.
func Diagrams(b *BM25, query string, d *Detail, candidates, rrfK int, shown map[string]bool) ([]DiagramHit, error) {
	db := b.DB
	pool := rowids(d.VectorHits)
	types := map[int64]string{}
	if len(pool) > 0 {
		args := make([]any, len(pool))
		for i, r := range pool {
			args[i] = r
		}
		rows, err := db.Conn.Query("SELECT c.rowid, d.type FROM chunks c JOIN documents d ON d.id = c.document_id "+
			"WHERE c.rowid IN ("+marks(len(pool))+")", args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var r int64
			var t string
			rows.Scan(&r, &t)
			types[r] = t
		}
		rows.Close()
	}
	cos := map[int64]float64{}
	for _, h := range d.VectorHits {
		if _, ok := cos[h.Rowid]; !ok {
			cos[h.Rowid] = h.Score
		}
	}
	var vec []int64
	for _, r := range pool {
		if t := types[r]; (t == "plantuml" || t == "mermaid") && len(vec) < candidates {
			vec = append(vec, r)
		}
	}
	bmHits, err := b.SearchRowids(query, candidates, DiagramTypes...)
	if err != nil {
		return nil, err
	}
	if len(vec) == 0 || len(bmHits) == 0 {
		return nil, nil
	}
	fused := RRF([]Ranking{{"bm25", rowids(bmHits), 1}, {"vector", vec, 1}}, rrfK)
	ids := make([]int64, len(fused))
	for i, f := range fused {
		ids[i] = f.Item
	}
	rows, err := db.ChunksByRowid(ids)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for p := range shown {
		seen[p] = true
	}
	var out []DiagramHit
	for _, f := range fused {
		r, ok := rows[f.Item]
		if !ok || seen[r.DocumentPath] {
			continue
		}
		seen[r.DocumentPath] = true // a file's best chunk decides; weaker chunks of it are skipped
		if f.Ranks["bm25"] == 0 || f.Ranks["vector"] == 0 || cos[f.Item] < DiagramMinCos {
			continue
		}
		h := DiagramHit{Path: r.DocumentPath}
		err := db.Conn.QueryRow("SELECT d.title, MIN(c.start_line), MAX(c.end_line) FROM documents d "+
			"JOIN chunks c ON c.document_id = d.id WHERE d.path = ?", r.DocumentPath).Scan(&h.Title, &h.StartLine, &h.EndLine)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
		if len(out) == MaxDiagrams {
			break
		}
	}
	return out, nil
}

func marks(n int) string {
	if n == 0 {
		return ""
	}
	b := make([]byte, 0, 2*n)
	for i := 0; i < n; i++ {
		b = append(b, '?', ',')
	}
	return string(b[:len(b)-1])
}
