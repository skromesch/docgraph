package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/skromesch/docgraph/internal/chunk"
	"github.com/skromesch/docgraph/internal/config"
	"github.com/skromesch/docgraph/internal/embed"
	"github.com/skromesch/docgraph/internal/index"
	"github.com/skromesch/docgraph/internal/store"
)

// newEmbedder builds the configured embedder. "torch" (a legacy backend name)
// maps to ONNX fp16: its vectors are interchangeable and share the model name.
func newEmbedder(cfg config.Config) (embed.Embedder, error) {
	e := cfg.Embedding
	backend := e.Backend
	if backend == "" || backend == "torch" {
		backend = "onnx-fp16"
	}
	return embed.NewONNX(embed.ONNXOptions{Model: e.Model, Backend: backend, BatchSize: e.BatchSize,
		QueryPrefix: e.QueryPrefix, PassagePrefix: e.PassagePrefix})
}

func progress(done, total int, label string) {
	r := []rune(label)
	if len(r) > 60 {
		r = r[:60]
	}
	fmt.Fprintf(os.Stderr, "\r  %-60s %d/%d", string(r), done, total)
}

func printSummary(s index.Summary) {
	fmt.Printf("Documents scanned: %d\nUnchanged: %d\nModified: %d\nNew: %d\nDeleted: %d\nFailed: %d\n"+
		"Chunks created: %d\nChunks embedded: %d\nEmbeddings reused: %d\nTime: %.2fs\n",
		s.Scanned, s.Unchanged, s.Modified, s.New, s.Deleted, s.Failed,
		s.ChunksCreated, s.ChunksEmbedded, s.EmbeddingsReused, s.Seconds)
	for _, e := range s.Errors {
		fmt.Fprintf(os.Stderr, "  error: %s\n", e)
	}
}

func indexCmd() *cobra.Command {
	var rebuild, noEmbed bool
	cmd := &cobra.Command{
		Use:   "index DIRECTORY",
		Short: "Index (incrementally) all supported files under DIRECTORY.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, cfg, db, err := openProject()
			if err != nil {
				return err
			}
			if rebuild {
				db.Close()
				for _, suf := range []string{"", "-wal", "-shm"} {
					os.Remove(config.DBPath(root) + suf)
				}
				if db, err = store.Open(config.DBPath(root)); err != nil {
					return err
				}
			}
			defer db.Close()
			dir, _ := filepath.Abs(args[0])
			if r, err := filepath.EvalSymlinks(dir); err == nil {
				dir = r
			}
			if prev, _ := db.GetMeta("source_root"); prev != "" && prev != dir {
				return fmt.Errorf("index was built from %s; use --rebuild to index another directory", prev)
			}
			var e embed.Embedder
			if !noEmbed {
				if e, err = newEmbedder(cfg); err != nil {
					return err
				}
				defer e.Close()
			}
			s, err := index.New(db, cfg, e, nil).Index(dir, progress, false)
			fmt.Fprintln(os.Stderr)
			if errors.Is(err, store.ErrEmbeddingMismatch) {
				return err
			}
			printSummary(s)
			return err
		},
	}
	cmd.Flags().BoolVar(&rebuild, "rebuild", false, "Drop the existing index first.")
	cmd.Flags().BoolVar(&noEmbed, "no-embed", false, "BM25 only; a later run embeds the backlog.")
	return cmd
}

func syncCmd() *cobra.Command {
	var noEmbed bool
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Bring the index up to date with the indexed directory (one incremental run).",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, cfg, db, err := openProject()
			if err != nil {
				return err
			}
			defer db.Close()
			source, _ := db.GetMeta("source_root")
			if st, err := os.Stat(source); source == "" || err != nil || !st.IsDir() {
				return errors.New("no indexed directory yet; run `docgraph index <dir>` first")
			}
			count := chunk.GetCounter(cfg.Embedding.Model)
			// first pass without the model (fast); load it only if something needs embedding
			s, err := index.New(db, cfg, nil, count).Index(source, nil, true)
			if err != nil {
				return err
			}
			var pending int
			db.Conn.QueryRow("SELECT COUNT(*) FROM chunks WHERE embedding_key IS NULL").Scan(&pending)
			if !noEmbed && pending > 0 {
				e, err := newEmbedder(cfg)
				if err != nil {
					return err
				}
				defer e.Close()
				es, err := index.New(db, cfg, e, count).Index(source, progress, true)
				fmt.Fprintln(os.Stderr)
				if err != nil {
					return err
				}
				s.ChunksEmbedded, s.EmbeddingsReused, s.Seconds = es.ChunksEmbedded, es.EmbeddingsReused, s.Seconds+es.Seconds
			}
			if !s.Changed() {
				fmt.Printf("Up to date (%d documents, %.2fs).\n", s.Scanned, s.Seconds)
				return nil
			}
			fmt.Printf("Synced %s: %d new, %d modified, %d deleted, %d failed; %d chunks, %d embedded (%d reused) in %.2fs\n",
				source, s.New, s.Modified, s.Deleted, s.Failed, s.ChunksCreated, s.ChunksEmbedded, s.EmbeddingsReused, s.Seconds)
			for _, e := range s.Errors {
				fmt.Fprintf(os.Stderr, "  error: %s\n", e)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&noEmbed, "no-embed", false, "Do not compute embeddings.")
	return cmd
}
