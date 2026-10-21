// Package store is the SQLite repository: documents, chunks, metadata, the
// embedding cache and the sqlite-vec vector table (schema.sql, docs/SCHEMA.md).
package store

import (
	"database/sql"
	_ "embed"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	_ "github.com/mattn/go-sqlite3"

	"github.com/skromesch/docgraph/internal/model"
)

//go:embed schema.sql
var schemaSQL string

const SchemaVersion = "1"

func init() { sqlite_vec.Auto() }

type DB struct {
	Path string
	Conn *sql.DB
}

// Open creates or opens a database and applies the schema. One connection only:
// callers serialize access, (and ":memory:" needs it).
func Open(path string) (*DB, error) {
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
	}
	conn, err := sql.Open("sqlite3", path+"?_foreign_keys=on&_journal_mode=WAL")
	if err != nil {
		return nil, err
	}
	conn.SetMaxOpenConns(1)
	if _, err := conn.Exec(schemaSQL); err != nil {
		conn.Close()
		if strings.Contains(err.Error(), "fts5") {
			return nil, fmt.Errorf("SQLite lacks FTS5; build with -tags sqlite_fts5: %w", err)
		}
		return nil, err
	}
	db := &DB{Path: path, Conn: conn}
	if v, _ := db.GetMeta("schema_version"); v == "" {
		if err := db.SetMeta("schema_version", SchemaVersion); err != nil {
			return nil, err
		}
	}
	return db, nil
}

func (d *DB) Close() error { return d.Conn.Close() }

// -- metadata ---------------------------------------------------------------

// GetMeta returns "" for a missing key.
func (d *DB) GetMeta(key string) (string, error) {
	var v string
	err := d.Conn.QueryRow("SELECT value FROM metadata WHERE key = ?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (d *DB) SetMeta(key, value string) error {
	_, err := d.Conn.Exec("INSERT INTO metadata(key, value) VALUES (?, ?) "+
		"ON CONFLICT(key) DO UPDATE SET value = excluded.value", key, value)
	return err
}

// -- documents --------------------------------------------------------------

// Documents returns all documents keyed by path.
func (d *DB) Documents() (map[string]model.Document, error) {
	rows, err := d.Conn.Query("SELECT id, path, title, type, content_hash, size_bytes, modified_at, indexed_at FROM documents")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]model.Document{}
	for rows.Next() {
		var doc model.Document
		if err := rows.Scan(&doc.ID, &doc.Path, &doc.Title, &doc.Type, &doc.ContentHash,
			&doc.SizeBytes, &doc.ModifiedAt, &doc.IndexedAt); err != nil {
			return nil, err
		}
		out[doc.Path] = doc
	}
	return out, rows.Err()
}

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// Tx runs fn in one transaction (the indexer commits per indexing phase).
func (d *DB) Tx(fn func(tx *Tx) error) error {
	t, err := d.Conn.Begin()
	if err != nil {
		return err
	}
	if err := fn(&Tx{t}); err != nil {
		t.Rollback()
		return err
	}
	return t.Commit()
}

// Tx exposes the write operations inside a transaction.
type Tx struct{ tx *sql.Tx }

func (t *Tx) SetMeta(key, value string) error {
	_, err := t.tx.Exec("INSERT INTO metadata(key, value) VALUES (?, ?) "+
		"ON CONFLICT(key) DO UPDATE SET value = excluded.value", key, value)
	return err
}

func (t *Tx) UpsertDocument(doc *model.Document) error {
	if doc.IndexedAt == 0 {
		doc.IndexedAt = UnixNow()
	}
	_, err := t.tx.Exec("INSERT INTO documents(id, path, title, type, content_hash, size_bytes, modified_at, indexed_at) "+
		"VALUES (?, ?, ?, ?, ?, ?, ?, ?) "+
		"ON CONFLICT(id) DO UPDATE SET title=excluded.title, type=excluded.type, "+
		"content_hash=excluded.content_hash, size_bytes=excluded.size_bytes, "+
		"modified_at=excluded.modified_at, indexed_at=excluded.indexed_at",
		doc.ID, doc.Path, doc.Title, doc.Type, doc.ContentHash, doc.SizeBytes, doc.ModifiedAt, doc.IndexedAt)
	return err
}

// DeleteDocument: chunks go via ON DELETE CASCADE, FTS rows via the chunks_ad trigger.
func (t *Tx) DeleteDocument(documentID string) error {
	_, err := t.tx.Exec("DELETE FROM documents WHERE id = ?", documentID)
	return err
}

func (t *Tx) ChunkRowids(documentID string) ([]int64, error) {
	return queryInts(t.tx, "SELECT rowid FROM chunks WHERE document_id = ?", documentID)
}

func (t *Tx) DeleteChunks(documentID string) error {
	_, err := t.tx.Exec("DELETE FROM chunks WHERE document_id = ?", documentID)
	return err
}

func (t *Tx) InsertChunk(c *model.Chunk, embeddingKey *string) (int64, error) {
	res, err := t.tx.Exec("INSERT INTO chunks(id, document_id, parent_chunk_id, heading, heading_path, heading_level, "+
		"content, embedding_text, content_hash, embedding_key, start_line, end_line, chunk_index, part, token_count) "+
		"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		c.ID, c.DocumentID, c.ParentChunkID, c.Heading, c.HeadingPath, c.HeadingLevel, c.Content,
		c.EmbeddingText, c.ContentHash, embeddingKey, c.StartLine, c.EndLine, c.ChunkIndex, c.Part, c.TokenCount)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (t *Tx) SetEmbeddingKey(rowid int64, key string) error {
	_, err := t.tx.Exec("UPDATE chunks SET embedding_key = ? WHERE rowid = ?", key, rowid)
	return err
}

func (t *Tx) PutCachedEmbedding(key, modelName string, vec []float32) error {
	_, err := t.tx.Exec("INSERT OR REPLACE INTO embedding_cache(key, model, dim, vector, created_at) VALUES (?,?,?,?,?)",
		key, modelName, len(vec), VectorToBlob(vec), UnixNow())
	return err
}

// PruneEmbeddingCache drops cached vectors that no chunk references any more.
func (t *Tx) PruneEmbeddingCache() (int64, error) {
	res, err := t.tx.Exec("DELETE FROM embedding_cache WHERE key NOT IN " +
		"(SELECT embedding_key FROM chunks WHERE embedding_key IS NOT NULL)")
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// -- reads ------------------------------------------------------------------

// ChunkRow is a chunk joined with its document path, as hydrated for search results.
type ChunkRow struct {
	Rowid        int64
	ID           string
	DocumentPath string
	Heading      string
	HeadingPath  string
	Content      string
	StartLine    int
	EndLine      int
	ChunkIndex   int
}

func (d *DB) ChunksByRowid(rowids []int64) (map[int64]ChunkRow, error) {
	out := map[int64]ChunkRow{}
	if len(rowids) == 0 {
		return out, nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(rowids)), ",")
	args := make([]any, len(rowids))
	for i, r := range rowids {
		args[i] = r
	}
	rows, err := d.Conn.Query("SELECT c.rowid, c.id, d.path, c.heading, c.heading_path, c.content, "+
		"c.start_line, c.end_line, c.chunk_index FROM chunks c JOIN documents d ON d.id = c.document_id "+
		"WHERE c.rowid IN ("+marks+")", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r ChunkRow
		if err := rows.Scan(&r.Rowid, &r.ID, &r.DocumentPath, &r.Heading, &r.HeadingPath, &r.Content,
			&r.StartLine, &r.EndLine, &r.ChunkIndex); err != nil {
			return nil, err
		}
		out[r.Rowid] = r
	}
	return out, rows.Err()
}

func (d *DB) CachedEmbeddings(keys []string) (map[string][]float32, error) {
	out := map[string][]float32{}
	for i := 0; i < len(keys); i += 500 {
		batch := keys[i:min(i+500, len(keys))]
		args := make([]any, len(batch))
		for j, k := range batch {
			args[j] = k
		}
		marks := strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")
		rows, err := d.Conn.Query("SELECT key, vector FROM embedding_cache WHERE key IN ("+marks+")", args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var k string
			var blob []byte
			if err := rows.Scan(&k, &blob); err != nil {
				rows.Close()
				return nil, err
			}
			out[k] = BlobToVector(blob)
		}
		rows.Close()
	}
	return out, nil
}

type Stats struct {
	Documents       int
	DocumentsByType map[string]int
	Chunks          int
	ChunksEmbedded  int
	AvgChunkTokens  float64
	MaxChunkTokens  int
	EmbeddingCache  int
	EmbeddingModel  string
	EmbeddingDim    string
	SchemaVersion   string
	SourceRoot      string
}

func (d *DB) Stats() (Stats, error) {
	var s Stats
	one := func(q string, dst any) error { return d.Conn.QueryRow(q).Scan(dst) }
	for q, dst := range map[string]any{
		"SELECT COUNT(*) FROM documents":                              &s.Documents,
		"SELECT COUNT(*) FROM chunks":                                 &s.Chunks,
		"SELECT COUNT(*) FROM chunks WHERE embedding_key IS NOT NULL": &s.ChunksEmbedded,
		"SELECT ROUND(COALESCE(AVG(token_count), 0), 1) FROM chunks":  &s.AvgChunkTokens,
		"SELECT COALESCE(MAX(token_count), 0) FROM chunks":            &s.MaxChunkTokens,
		"SELECT COUNT(*) FROM embedding_cache":                        &s.EmbeddingCache,
	} {
		if err := one(q, dst); err != nil {
			return s, err
		}
	}
	s.DocumentsByType = map[string]int{}
	rows, err := d.Conn.Query("SELECT type, COUNT(*) FROM documents GROUP BY type")
	if err != nil {
		return s, err
	}
	defer rows.Close()
	for rows.Next() {
		var t string
		var n int
		rows.Scan(&t, &n)
		s.DocumentsByType[t] = n
	}
	s.EmbeddingModel, _ = d.GetMeta("embedding_model")
	s.EmbeddingDim, _ = d.GetMeta("embedding_dim")
	s.SchemaVersion, _ = d.GetMeta("schema_version")
	s.SourceRoot, _ = d.GetMeta("source_root")
	return s, nil
}

// -- helpers ----------------------------------------------------------------

func queryInts(q execer, query string, args ...any) ([]int64, error) {
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// VectorToBlob encodes little-endian float32, the format sqlite-vec uses.
func VectorToBlob(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(f))
	}
	return b
}

func BlobToVector(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v
}

func UnixNow() float64 { return float64(time.Now().UnixNano()) / 1e9 }
