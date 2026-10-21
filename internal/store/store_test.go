package store

import (
	"errors"
	"testing"

	"github.com/skromesch/docgraph/internal/model"
)

func TestDocumentsChunksFTSAndCascade(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	doc := model.Document{ID: "d1", Path: "Inventory.md", Title: "Inventory", Type: "markdown", ContentHash: "h", ModifiedAt: 1.5}
	err = db.Tx(func(tx *Tx) error {
		if err := tx.UpsertDocument(&doc); err != nil {
			return err
		}
		_, err := tx.InsertChunk(&model.Chunk{ID: "c1", DocumentID: "d1", Heading: "Closing",
			HeadingPath: "Inventory > Closing", Content: "Closing the café inventory finalizes its résumé.",
			EmbeddingText: "x", ContentHash: "h", StartLine: 1, EndLine: 2}, nil)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var n int
	// remove_diacritics 2: "resume" matches "résumé"
	db.Conn.QueryRow("SELECT COUNT(*) FROM chunks_fts WHERE chunks_fts MATCH 'resume'").Scan(&n)
	if n != 1 {
		t.Fatalf("fts hits = %d", n)
	}
	docs, _ := db.Documents()
	if docs["Inventory.md"].ModifiedAt != 1.5 {
		t.Fatalf("documents: %+v", docs)
	}
	db.Tx(func(tx *Tx) error { return tx.DeleteDocument("d1") })
	db.Conn.QueryRow("SELECT COUNT(*) FROM chunks").Scan(&n)
	if n != 0 {
		t.Fatal("cascade failed")
	}
	db.Conn.QueryRow("SELECT COUNT(*) FROM chunks_fts WHERE chunks_fts MATCH 'resume'").Scan(&n)
	if n != 0 {
		t.Fatal("fts not cleaned up")
	}
}

func TestVectorStore(t *testing.T) {
	db, _ := Open(":memory:")
	defer db.Close()
	vs := SqliteVec{db}
	err := db.Tx(func(tx *Tx) error {
		if err := vs.Ensure(tx, "m", 3); err != nil {
			return err
		}
		vs.Upsert(tx, 1, []float32{1, 0, 0})
		vs.Upsert(tx, 2, []float32{0, 1, 0})
		return vs.Upsert(tx, 1, []float32{0.6, 0.8, 0}) // replace
	})
	if err != nil {
		t.Fatal(err)
	}
	hits, err := vs.Search([]float32{0, 1, 0}, 2)
	if err != nil || len(hits) != 2 || hits[0].Rowid != 2 || hits[0].Score < 0.999 || hits[1].Score < 0.79 {
		t.Fatalf("hits %+v err %v", hits, err)
	}
	err = db.Tx(func(tx *Tx) error { return vs.Ensure(tx, "other", 3) })
	if !errors.Is(err, ErrEmbeddingMismatch) {
		t.Fatalf("want mismatch, got %v", err)
	}
}
