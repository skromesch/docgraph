// Package embed computes text embeddings. The ONNX implementation reproduces
// sentence-transformers' pipeline for e5: prefix, tokenize (with special tokens,
// truncated to max length), transformer, mean pooling over the attention mask,
// L2 normalization.
package embed

import (
	"math"
	"strings"
)

type Kind string

const (
	Query   Kind = "query"
	Passage Kind = "passage"
)

type Embedder interface {
	// Name identifies the vectors (stored as metadata.embedding_model and in embedding keys).
	Name() string
	Dim() int
	Prefix(kind Kind) string
	// Embed prefixes the texts itself; returned vectors are L2-normalized.
	Embed(texts []string, kind Kind) ([][]float32, error)
	Close() error
}

// DefaultPrefixes: e5 models need "query: " / "passage: ".
func DefaultPrefixes(model string) map[Kind]string {
	name := strings.ToLower(model)
	if strings.Contains(name, "e5") && !strings.Contains(name, "instruct") {
		return map[Kind]string{Query: "query: ", Passage: "passage: "}
	}
	return map[Kind]string{Query: "", Passage: ""}
}

// Normalize scales v to unit length in place (zero vectors stay zero).
func Normalize(v []float32) {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return
	}
	inv := float32(1 / math.Sqrt(sum))
	for i := range v {
		v[i] *= inv
	}
}
