// Package fixture builds indexes of the test wiki for package tests.
package fixture

import (
	"path/filepath"
	"testing"

	"github.com/skromesch/docgraph/internal/chunk"
	"github.com/skromesch/docgraph/internal/config"
	"github.com/skromesch/docgraph/internal/embed"
	"github.com/skromesch/docgraph/internal/index"
	"github.com/skromesch/docgraph/internal/store"
	"github.com/skromesch/docgraph/internal/testutil"
)

const Model = "intfloat/multilingual-e5-base"

// Embedder loads the fp16 ONNX model, skipping the test when it is not pulled.
func Embedder(t testing.TB) embed.Embedder {
	t.Helper()
	e, err := embed.NewONNX(embed.ONNXOptions{Model: Model, Backend: "onnx-fp16"})
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

// Index indexes the fixture wiki into a fresh database (heuristic token
// counter; e may be nil for a keyword-only index) and returns it with the root.
func Index(t testing.TB, e embed.Embedder) (*store.DB, string) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "docgraph.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	root := testutil.Fixtures()
	if _, err := index.New(db, config.Default(), e, chunk.Heuristic).Index(root, nil, false); err != nil {
		t.Fatal(err)
	}
	return db, root
}
