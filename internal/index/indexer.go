package index

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/skromesch/docgraph/internal/chunk"
	"github.com/skromesch/docgraph/internal/config"
	"github.com/skromesch/docgraph/internal/convert"
	"github.com/skromesch/docgraph/internal/embed"
	"github.com/skromesch/docgraph/internal/hashing"
	"github.com/skromesch/docgraph/internal/model"
	"github.com/skromesch/docgraph/internal/parse"
	"github.com/skromesch/docgraph/internal/store"
	"github.com/skromesch/docgraph/internal/textutil"
)

type Summary struct {
	Scanned, Unchanged, Modified, New, Deleted, Failed int
	Skipped                                            int      // .docx left out: no pandoc
	SkippedDocx                                        []string // their paths
	ChunksCreated, ChunksEmbedded, EmbeddingsReused    int
	Seconds                                            float64
	Errors                                             []string
}

// Changed reports whether the run touched the index.
func (s Summary) Changed() bool {
	return s.New+s.Modified+s.Deleted+s.Failed+s.ChunksEmbedded+s.EmbeddingsReused > 0
}

// Progress is called with (done, total, label).
type Progress func(done, total int, label string)

type Indexer struct {
	DB         *store.DB
	Cfg        config.Config
	Embedder   embed.Embedder // nil: BM25 only; chunks stay pending for a later run
	Count      chunk.Counter
	ConvertDir string // hidden Markdown of .docx files
	Pandoc     string // "" = not available: .docx files are skipped
	vectors    store.SqliteVec
}

func New(db *store.DB, cfg config.Config, e embed.Embedder, count chunk.Counter) *Indexer {
	if count == nil {
		count = chunk.GetCounter(cfg.Embedding.Model)
	}
	// deletions always reach the vector table, even without an embedder
	pandoc, _ := convert.FindPandoc(cfg.Convert.PandocPath)
	return &Indexer{DB: db, Cfg: cfg, Embedder: e, Count: count, ConvertDir: convert.Dir(db.Path), Pandoc: pandoc,
		vectors: store.SqliteVec{DB: db}}
}

// Mtime is the modification time in float seconds (sec + nsec * 1e-9), the
// value stored for the quick check.
func Mtime(st os.FileInfo) float64 {
	t := st.ModTime()
	return float64(t.Unix()) + float64(t.Nanosecond())*1e-9
}

// Index runs one incremental pass. quickCheck skips files whose size and mtime
// match the stored values without reading them (sync); otherwise every file is hashed.
func (ix *Indexer) Index(root string, progress Progress, quickCheck bool) (Summary, error) {
	t0 := time.Now()
	var s Summary
	root, err := filepath.Abs(root)
	if err != nil {
		return s, err
	}
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	existing, err := ix.DB.Documents()
	if err != nil {
		return s, err
	}
	ic := ix.Cfg.Index
	files, err := Scan(root, ic.Extensions, ic.ExcludeDirs, ic.Ignore)
	if err != nil {
		return s, err
	}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths) // UTF-8 byte order = code point order
	s.Scanned = len(paths)
	opts := chunk.Options{TargetTokens: ic.TargetTokens, MaxTokens: ic.MaxTokens,
		OverlapTokens: ic.OverlapTokens, MinSectionTokens: ic.MinSectionTokens}

	for i, rel := range paths {
		if progress != nil {
			progress(i+1, len(paths), rel)
		}
		full := files[rel]
		old, had := existing[rel]
		st, err := os.Stat(full)
		if err != nil {
			s.Failed++
			s.Errors = append(s.Errors, fmt.Sprintf("%s: %v", rel, err))
			continue
		}
		if quickCheck && had && st.Size() == old.SizeBytes && Mtime(st) == old.ModifiedAt {
			s.Unchanged++
			continue
		}
		raw, err := os.ReadFile(full)
		if err != nil {
			s.Failed++
			s.Errors = append(s.Errors, fmt.Sprintf("%s: %v", rel, err))
			continue
		}
		contentHash := hashing.SHA256Bytes(raw)
		if had && old.ContentHash == contentHash {
			s.Unchanged++
			continue
		}
		text := textutil.DecodeFile(raw)
		if convert.IsDocx(rel) {
			if ix.Pandoc == "" { // keep any indexed version until pandoc is back
				s.Skipped++
				s.SkippedDocx = append(s.SkippedDocx, rel)
				continue
			}
			if text, err = ix.convertDocx(rel, full); err != nil {
				s.Failed++
				s.Errors = append(s.Errors, fmt.Sprintf("%s: %v", rel, err))
				continue
			}
		}
		n, err := ix.indexDocument(rel, text, len(raw), contentHash, st, opts)
		if err != nil {
			s.Failed++
			s.Errors = append(s.Errors, fmt.Sprintf("%s: %v", rel, err))
			continue
		}
		s.ChunksCreated += n
		if had {
			s.Modified++
		} else {
			s.New++
		}
	}

	var gone []string
	for rel := range existing {
		if _, ok := files[rel]; !ok {
			gone = append(gone, rel)
		}
	}
	sort.Strings(gone)
	for _, rel := range gone {
		id := existing[rel].ID
		if err := ix.DB.Tx(func(tx *store.Tx) error { return ix.deleteDocument(tx, id) }); err != nil {
			return s, err
		}
		if convert.IsDocx(rel) {
			os.Remove(convert.Path(ix.ConvertDir, rel))
		}
		s.Deleted++
	}

	if ix.Embedder != nil {
		embedded, reused, err := ix.embedPending(progress)
		if err != nil {
			return s, err
		}
		s.ChunksEmbedded, s.EmbeddingsReused = embedded, reused
	}
	err = ix.DB.Tx(func(tx *store.Tx) error {
		if err := ix.vectors.PruneOrphans(tx); err != nil {
			return err
		}
		// only after embedding: pending chunks (embedding_key NULL) of a no-embed
		// pass would otherwise lose cached vectors they are about to reuse
		if ix.Embedder != nil {
			if _, err := tx.PruneEmbeddingCache(); err != nil {
				return err
			}
		}
		if err := tx.SetMeta("source_root", root); err != nil {
			return err
		}
		return tx.SetMeta("last_indexed_at", strconv.FormatFloat(store.UnixNow(), 'f', -1, 64))
	})
	s.Seconds = float64(time.Since(t0).Milliseconds()) / 1000
	return s, err
}

// indexDocument re-chunks one file in its own transaction (a crash leaves the
// previous version or the new one, never a mix).
func (ix *Indexer) indexDocument(rel, text string, size int, contentHash string, st os.FileInfo, opts chunk.Options) (int, error) {
	p := parse.For(rel)
	if p == nil {
		return 0, fmt.Errorf("no parser for %s", rel)
	}
	parsed := p.Parse(rel, text)
	chunks := chunk.Document(rel, parsed, opts, ix.Count)
	doc := model.Document{ID: hashing.DocumentID(rel), Path: rel, Title: parsed.Title, Type: parsed.Type,
		ContentHash: contentHash, SizeBytes: int64(size), ModifiedAt: Mtime(st), IndexedAt: store.UnixNow()}
	err := ix.DB.Tx(func(tx *store.Tx) error {
		if err := ix.deleteChunks(tx, doc.ID); err != nil {
			return err
		}
		if err := tx.UpsertDocument(&doc); err != nil {
			return err
		}
		for i := range chunks {
			if _, err := tx.InsertChunk(&chunks[i], nil); err != nil {
				return err
			}
		}
		return nil
	})
	return len(chunks), err
}

func (ix *Indexer) deleteChunks(tx *store.Tx, documentID string) error {
	rowids, err := tx.ChunkRowids(documentID)
	if err != nil {
		return err
	}
	if err := ix.vectors.Delete(tx, rowids); err != nil {
		return err
	}
	return tx.DeleteChunks(documentID)
}

func (ix *Indexer) deleteDocument(tx *store.Tx, documentID string) error {
	if err := ix.deleteChunks(tx, documentID); err != nil {
		return err
	}
	return tx.DeleteDocument(documentID)
}

// embedPending embeds every chunk without an embedding_key. Vectors are looked
// up in embedding_cache first, so only genuinely new text hits the model.
// Returns (newly embedded, reused from cache).
func (ix *Indexer) embedPending(progress Progress) (int, int, error) {
	e := ix.Embedder
	if err := ix.DB.Tx(func(tx *store.Tx) error { return ix.vectors.Ensure(tx, e.Name(), e.Dim()) }); err != nil {
		return 0, 0, err
	}
	prefix := e.Prefix(embed.Passage)
	rows, err := ix.DB.Conn.Query("SELECT rowid, embedding_text FROM chunks WHERE embedding_key IS NULL ORDER BY rowid")
	if err != nil {
		return 0, 0, err
	}
	type pending struct {
		rowid int64
		key   string
	}
	var todoRows []pending
	texts := map[string]string{} // key -> text
	var keys []string
	for rows.Next() {
		var p pending
		var text string
		if err := rows.Scan(&p.rowid, &text); err != nil {
			rows.Close()
			return 0, 0, err
		}
		p.key = hashing.EmbeddingKey(e.Name(), prefix+text)
		todoRows = append(todoRows, p)
		if _, ok := texts[p.key]; !ok {
			texts[p.key] = text
			keys = append(keys, p.key)
		}
	}
	rows.Close()
	if len(todoRows) == 0 {
		return 0, 0, nil
	}
	cached, err := ix.DB.CachedEmbeddings(keys)
	if err != nil {
		return 0, 0, err
	}
	var todo []string // keys to embed, first-seen order, de-duplicated
	for _, k := range keys {
		if _, ok := cached[k]; !ok {
			todo = append(todo, k)
		}
	}
	batch := max(1, ix.Cfg.Embedding.BatchSize) * 8
	for i := 0; i < len(todo); i += batch {
		part := todo[i:min(i+batch, len(todo))]
		in := make([]string, len(part))
		for j, k := range part {
			in[j] = texts[k]
		}
		vecs, err := e.Embed(in, embed.Passage)
		if err != nil {
			return 0, 0, err
		}
		err = ix.DB.Tx(func(tx *store.Tx) error {
			for j, k := range part {
				cached[k] = vecs[j]
				if err := tx.PutCachedEmbedding(k, e.Name(), vecs[j]); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return 0, 0, err
		}
		if progress != nil {
			progress(min(i+batch, len(todo)), len(todo), "embedding")
		}
	}
	err = ix.DB.Tx(func(tx *store.Tx) error {
		for _, p := range todoRows {
			if err := ix.vectors.Upsert(tx, p.rowid, cached[p.key]); err != nil {
				return err
			}
			if err := tx.SetEmbeddingKey(p.rowid, p.key); err != nil {
				return err
			}
		}
		return nil
	})
	return len(todo), len(todoRows) - len(todo), err
}

// convertDocx runs pandoc and writes the hidden Markdown atomically; the
// returned text is what gets indexed and what read_document serves.
func (ix *Indexer) convertDocx(rel, full string) (string, error) {
	text, err := convert.Docx(ix.Pandoc, full)
	if err != nil {
		return "", err
	}
	return text, convert.Write(convert.Path(ix.ConvertDir, rel), text)
}
