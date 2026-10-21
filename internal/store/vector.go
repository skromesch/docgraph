package store

import (
	"errors"
	"fmt"
	"strconv"
)

const VecTable = "chunk_embeddings"

// ErrEmbeddingMismatch: the index holds vectors from another model or dimension.
var ErrEmbeddingMismatch = errors.New("embedding model mismatch")

// VectorStore keeps L2-normalized vectors keyed by chunks.rowid.
type VectorStore interface {
	Ensure(tx *Tx, model string, dim int) error
	Upsert(tx *Tx, rowid int64, vec []float32) error
	Delete(tx *Tx, rowids []int64) error
	Search(query []float32, k int) ([]Hit, error)
	Count() (int, error)
}

// Hit is (rowid, score); for vectors the score is cosine similarity = 1 - distance.
type Hit struct {
	Rowid int64
	Score float64
}

type SqliteVec struct{ DB *DB }

func (s SqliteVec) exists(q execer) bool {
	var one int
	return q.QueryRow("SELECT 1 FROM sqlite_master WHERE name = ?", VecTable).Scan(&one) == nil
}

// Ensure creates the vec0 table, or fails if it holds vectors from another model.
func (s SqliteVec) Ensure(tx *Tx, model string, dim int) error {
	var storedModel, storedDim string
	tx.tx.QueryRow("SELECT value FROM metadata WHERE key = 'embedding_model'").Scan(&storedModel)
	tx.tx.QueryRow("SELECT value FROM metadata WHERE key = 'embedding_dim'").Scan(&storedDim)
	if storedModel != "" && (storedModel != model || storedDim != strconv.Itoa(dim)) {
		return fmt.Errorf("%w: index was built with %s (dim %s), config requests %s (dim %d); run `docgraph index --rebuild`",
			ErrEmbeddingMismatch, storedModel, storedDim, model, dim)
	}
	if !s.exists(tx.tx) {
		if _, err := tx.tx.Exec(fmt.Sprintf(
			"CREATE VIRTUAL TABLE %s USING vec0(embedding float[%d] distance_metric=cosine)", VecTable, dim)); err != nil {
			return err
		}
	}
	if err := tx.SetMeta("embedding_model", model); err != nil {
		return err
	}
	return tx.SetMeta("embedding_dim", strconv.Itoa(dim))
}

func (s SqliteVec) Reset(tx *Tx) error {
	if _, err := tx.tx.Exec("DROP TABLE IF EXISTS " + VecTable); err != nil {
		return err
	}
	_, err := tx.tx.Exec("DELETE FROM metadata WHERE key IN ('embedding_model', 'embedding_dim')")
	return err
}

func (s SqliteVec) Upsert(tx *Tx, rowid int64, vec []float32) error {
	if _, err := tx.tx.Exec("DELETE FROM "+VecTable+" WHERE rowid = ?", rowid); err != nil {
		return err
	}
	_, err := tx.tx.Exec("INSERT INTO "+VecTable+"(rowid, embedding) VALUES (?, ?)", rowid, VectorToBlob(vec))
	return err
}

func (s SqliteVec) Delete(tx *Tx, rowids []int64) error {
	if len(rowids) == 0 || !s.exists(tx.tx) {
		return nil
	}
	for _, r := range rowids {
		if _, err := tx.tx.Exec("DELETE FROM "+VecTable+" WHERE rowid = ?", r); err != nil {
			return err
		}
	}
	return nil
}

// Search returns hits best first.
func (s SqliteVec) Search(query []float32, k int) ([]Hit, error) {
	if !s.exists(s.DB.Conn) {
		return nil, nil
	}
	rows, err := s.DB.Conn.Query("SELECT rowid, distance FROM "+VecTable+
		" WHERE embedding MATCH ? AND k = ? ORDER BY distance", VectorToBlob(query), k)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		var h Hit
		var dist float64
		if err := rows.Scan(&h.Rowid, &dist); err != nil {
			return nil, err
		}
		h.Score = 1 - dist
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s SqliteVec) Count() (int, error) {
	if !s.exists(s.DB.Conn) {
		return 0, nil
	}
	var n int
	err := s.DB.Conn.QueryRow("SELECT COUNT(*) FROM " + VecTable).Scan(&n)
	return n, err
}

// PruneOrphans deletes vectors whose chunk no longer exists.
func (s SqliteVec) PruneOrphans(tx *Tx) error {
	if !s.exists(tx.tx) {
		return nil
	}
	_, err := tx.tx.Exec("DELETE FROM " + VecTable + " WHERE rowid NOT IN (SELECT rowid FROM chunks)")
	return err
}
