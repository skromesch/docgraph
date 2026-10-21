package embed

import (
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/skromesch/docgraph/internal/testutil"
)

const model = "intfloat/multilingual-e5-base"

func cos(a, b []float32) float64 {
	var s float64
	for i := range a {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}

func newONNX(t *testing.T, backend string) *ONNX {
	e, err := NewONNX(ONNXOptions{Model: model, Backend: backend})
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

var goldenTexts = []struct {
	Kind Kind   `json:"kind"`
	Text string `json:"text"`
}{
	{Query, "How can an inventory be closed?"},
	{Passage, "Document: Inventory (Inventory.md)\nSection: Inventory > Closing an inventory\n\nAn inventory can be closed when every location is synchronized."},
	{Passage, "@startuml\n[*] --> Open\nOpen --> Closed : close\n@enduml"},
	{Query, "push transactions"},
	{Passage, strings.Repeat("A long text to test truncation. ", 200)}, // > 512 tokens
}

// Vectors must stay those of the fp32 model (testdata/golden/embed-vectors.json,
// written by -update with the onnx backend; the Go embedder matched PyTorch
// sentence-transformers vectors, cos >= 0.99998). fp16 must stay close to them.
func TestVectorsGolden(t *testing.T) {
	embedAll := func(e *ONNX) [][]float32 {
		out := make([][]float32, len(goldenTexts))
		for i, g := range goldenTexts {
			v, err := e.Embed([]string{g.Text}, g.Kind)
			if err != nil {
				t.Fatal(err)
			}
			out[i] = v[0]
		}
		return out
	}
	if testutil.Update() {
		vecs := embedAll(newONNX(t, "onnx"))
		rounded := make([][]float64, len(vecs))
		for i, v := range vecs {
			for _, x := range v {
				rounded[i] = append(rounded[i], testutil.Round(float64(x), 6))
			}
		}
		testutil.GoldenJSON(t, "embed-vectors.json", rounded)
		return
	}
	data, err := os.ReadFile(testutil.GoldenPath("embed-vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var want [][]float32
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatal(err)
	}
	ran := 0
	for backend, minWant := range map[string]float64{"onnx": 0.99999, "onnx-fp16": 0.999} {
		e, err := NewONNX(ONNXOptions{Model: model, Backend: backend})
		if err != nil {
			t.Logf("%s: %v", backend, err)
			continue
		}
		ran++
		got := embedAll(e)
		e.Close()
		minCos := 1.0
		for i := range got {
			minCos = math.Min(minCos, cos(got[i], want[i]))
		}
		t.Logf("%-9s min cosine vs golden %.6f", backend, minCos)
		if minCos < minWant {
			t.Errorf("%s: min cosine %.6f < %v", backend, minCos, minWant)
		}
	}
	if ran == 0 {
		t.Skip("no ONNX model available")
	}
}

func TestQueryPrefixAndNormalization(t *testing.T) {
	e := newONNX(t, "onnx-fp16")
	if e.Name() != model || e.Dim() != 768 || e.Prefix(Query) != "query: " {
		t.Fatalf("name %s dim %d prefix %q", e.Name(), e.Dim(), e.Prefix(Query))
	}
	v, _ := e.Embed([]string{"How can an inventory be closed?", ""}, Query)
	if n := cos(v[0], v[0]); math.Abs(n-1) > 1e-5 {
		t.Fatalf("not normalized: %f", n)
	}
	q, _ := e.Embed([]string{"closing an inventory"}, Query)
	p, _ := e.Embed([]string{"closing an inventory"}, Passage)
	if cos(q[0], p[0]) > 0.9999 {
		t.Fatal("query/passage prefixes not applied")
	}
}
