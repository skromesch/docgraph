package search

import (
	"github.com/skromesch/docgraph/internal/embed"
	"github.com/skromesch/docgraph/internal/model"
	"github.com/skromesch/docgraph/internal/store"
	"github.com/skromesch/docgraph/internal/textutil"
)

// Vector: embed the query, k-NN in the vector store.
type Vector struct {
	DB       *store.DB
	Embedder embed.Embedder
	Store    store.VectorStore
}

// NewVector checks (and records) that the index holds this embedder's vectors.
func NewVector(db *store.DB, e embed.Embedder, vs store.VectorStore) (*Vector, error) {
	err := db.Tx(func(tx *store.Tx) error { return vs.Ensure(tx, e.Name(), e.Dim()) })
	if err != nil {
		return nil, err
	}
	return &Vector{db, e, vs}, nil
}

func (v *Vector) SearchRowids(query string, k int) ([]store.Hit, error) {
	if textutil.Strip(query) == "" {
		return nil, nil
	}
	q, err := v.Embedder.Embed([]string{query}, embed.Query)
	if err != nil {
		return nil, err
	}
	return v.Store.Search(q[0], k)
}

func (v *Vector) Search(query string, k int) ([]model.SearchResult, error) {
	hits, err := v.SearchRowids(query, k)
	if err != nil {
		return nil, err
	}
	return Hydrate(v.DB, hits, "vector")
}
