package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/skromesch/docgraph/internal/model"
	"github.com/skromesch/docgraph/internal/search"
	"github.com/skromesch/docgraph/internal/store"
)

func searchCmd() *cobra.Command {
	var mode string
	var topK, preview int
	cmd := &cobra.Command{
		Use:   "search QUERY",
		Short: "Search the index.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, cfg, db, err := openProject()
			if err != nil {
				return err
			}
			defer db.Close()
			if mode == "" {
				mode = cfg.Search.Mode
			}
			k := topK
			if k <= 0 {
				k = cfg.Search.TopK
			}
			bm25 := &search.BM25{DB: db, Cfg: cfg.Search.BM25}
			var results []model.SearchResult
			switch mode {
			case "keyword":
				results, err = bm25.Search(args[0], k)
			case "semantic", "hybrid":
				e, err := newEmbedder(cfg)
				if err != nil {
					return err
				}
				defer e.Close()
				vec, err := search.NewVector(db, e, store.SqliteVec{DB: db})
				if err != nil {
					return err
				}
				if mode == "semantic" {
					results, err = vec.Search(args[0], k)
				} else {
					results, err = (&search.Hybrid{BM25: bm25, Vector: vec, Cfg: cfg.Search}).Search(args[0], k)
				}
				if err != nil {
					return err
				}
			default:
				return fmt.Errorf("mode %q is not available (keyword, semantic, hybrid)", mode)
			}
			if err != nil {
				return err
			}
			if len(results) == 0 {
				fmt.Println("No results.")
			}
			for _, r := range results {
				text := strings.Join(strings.Fields(r.Content), " ")
				snippet := text
				if rs := []rune(text); len(rs) > preview {
					snippet = string(rs[:preview]) + "…"
				}
				fmt.Printf("\x1b[1m%d. %s\x1b[0m\n", r.Rank, r.DocumentPath)
				fmt.Printf("   %s\n   lines %d-%d\n   score: %.4f%s\n   %s\n\n",
					r.HeadingPath, r.StartLine, r.EndLine, r.Score, ranks(r), snippet)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&mode, "mode", "m", "", "keyword | semantic | hybrid (default from config)")
	cmd.Flags().IntVarP(&topK, "top-k", "k", 0, "Number of results.")
	cmd.Flags().IntVar(&preview, "preview", 200, "Preview length in characters.")
	return cmd
}

func ranks(r model.SearchResult) string {
	if len(r.Ranks) < 2 {
		return ""
	}
	var parts []string
	for _, n := range []string{"bm25", "vector"} {
		if v := r.Ranks[n]; v > 0 {
			parts = append(parts, fmt.Sprintf("%s #%d", n, v))
		} else {
			parts = append(parts, n+" -")
		}
	}
	return "  (" + strings.Join(parts, ", ") + ")"
}
