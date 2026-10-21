package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/skromesch/docgraph/internal/chunk"
	"github.com/skromesch/docgraph/internal/config"
	"github.com/skromesch/docgraph/internal/embed"
	"github.com/skromesch/docgraph/internal/eval"
	"github.com/skromesch/docgraph/internal/mcpserver"
	"github.com/skromesch/docgraph/internal/search"
	"github.com/skromesch/docgraph/internal/service"
	"github.com/skromesch/docgraph/internal/store"
)

var version = "dev" // set by -ldflags "-X main.version=..."

func mcpCmd() *cobra.Command {
	var keywordOnly, noSync bool
	var project string
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Run the MCP server on stdio (for Claude Code / other MCP clients).",
		RunE: func(cmd *cobra.Command, args []string) error {
			if project != "" {
				if err := os.Chdir(project); err != nil {
					return err
				}
			}
			_, cfg, db, err := openProject()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(db, cfg, "")
			if !noSync {
				svc.EnableAutoSync(chunk.GetCounter(cfg.Embedding.Model))
			}
			if keywordOnly {
				svc.SetEmbedder(nil)
			} else {
				svc.LoadModelInBackground(func() (embed.Embedder, error) { return newEmbedder(cfg) })
			}
			return mcpserver.RunStdio(context.Background(), mcpserver.Build(svc, version))
		},
	}
	cmd.Flags().BoolVar(&keywordOnly, "keyword-only", false, "Do not load the embedding model (BM25 only).")
	cmd.Flags().BoolVar(&noSync, "no-sync", false, "Do not re-index changed files before tool calls.")
	cmd.Flags().StringVarP(&project, "project", "p", "", "Project dir containing .docgraph/ (default: cwd).")
	return cmd
}

// applyOverrides sets dotted keys ("search.rrf.k=20") through the YAML form of
// the config, so every key the file accepts can be overridden.
func applyOverrides(cfg config.Config, sets []string) (config.Config, error) {
	if len(sets) == 0 {
		return cfg, nil
	}
	raw, _ := yaml.Marshal(cfg)
	var tree map[string]any
	yaml.Unmarshal(raw, &tree)
	for _, s := range sets {
		key, val, ok := strings.Cut(s, "=")
		if !ok {
			return cfg, fmt.Errorf("--set %q: expected key=value", s)
		}
		var v any
		if err := yaml.Unmarshal([]byte(val), &v); err != nil {
			return cfg, err
		}
		parts := strings.Split(strings.TrimSpace(key), ".")
		node := tree
		for _, p := range parts[:len(parts)-1] {
			next, ok := node[p].(map[string]any)
			if !ok {
				return cfg, fmt.Errorf("unknown config key: %s", key)
			}
			node = next
		}
		last := parts[len(parts)-1]
		if _, ok := node[last]; !ok {
			return cfg, fmt.Errorf("unknown config key: %s", key)
		}
		if _, isMap := node[last].(map[string]any); isMap {
			return cfg, fmt.Errorf("unknown config key: %s", key)
		}
		node[last] = v
	}
	raw, _ = yaml.Marshal(tree)
	out := config.Default()
	err := yaml.Unmarshal(raw, &out)
	return out, err
}

func evalCmd() *cobra.Command {
	var modes, failures, output string
	var sets []string
	cmd := &cobra.Command{
		Use:   "eval DATASET",
		Short: "Evaluate retrieval quality (Recall@k, MRR) against a labelled dataset.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, cfg, db, err := openProject()
			if err != nil {
				return err
			}
			defer db.Close()
			if cfg, err = applyOverrides(cfg, sets); err != nil {
				return err
			}
			cases, err := eval.LoadDataset(args[0])
			if err != nil {
				return err
			}
			problems, err := eval.Validate(cases, db)
			if err != nil {
				return err
			}
			for _, p := range problems {
				fmt.Fprintf(os.Stderr, "  dataset warning: %s\n", p)
			}
			bm25 := &search.BM25{DB: db, Cfg: cfg.Search.BM25}
			funcs := map[string]eval.SearchFunc{"keyword": bm25.Search}
			var modeList []string
			for _, m := range strings.Split(modes, ",") {
				if m = strings.TrimSpace(m); m != "" {
					modeList = append(modeList, m)
				}
			}
			for _, m := range modeList {
				if m != "keyword" && funcs["semantic"] == nil {
					e, err := newEmbedder(cfg)
					if err != nil {
						return err
					}
					defer e.Close()
					vec, err := search.NewVector(db, e, store.SqliteVec{DB: db})
					if err != nil {
						return err
					}
					funcs["semantic"] = vec.Search
					funcs["hybrid"] = (&search.Hybrid{BM25: bm25, Vector: vec, Cfg: cfg.Search}).Search
				}
			}
			var rows []eval.Row
			for _, m := range modeList {
				f, ok := funcs[m]
				if !ok {
					return fmt.Errorf("unknown mode %q (keyword, semantic, hybrid)", m)
				}
				mr, err := eval.Evaluate(m, f, cases)
				if err != nil {
					return err
				}
				rows = append(rows, eval.Row{Label: m, Result: mr})
			}
			fmt.Printf("\nDataset: %s (%d queries)\n", args[0], len(cases))
			fmt.Printf("\nSection level (expected section or a subsection of it):\n%s\n", eval.FormatTable(rows, "section"))
			fmt.Printf("\nDocument level (expected document anywhere):\n%s\n", eval.FormatTable(rows, "document"))
			for _, r := range rows {
				if r.Label == failures {
					fmt.Printf("\nMisses for %s:\n%s\n", r.Label, eval.FormatFailures(r.Result, 20))
				}
			}
			if output != "" {
				data, err := eval.ToJSON(rows)
				if err != nil {
					return err
				}
				if err := os.WriteFile(output, data, 0o644); err != nil {
					return err
				}
				fmt.Printf("\nWrote %s\n", output)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&modes, "modes", "keyword,semantic,hybrid", "Comma separated.")
	cmd.Flags().StringArrayVar(&sets, "set", nil, "Config override, e.g. search.rrf.k=20 (repeatable).")
	cmd.Flags().StringVar(&failures, "failures", "", "Print misses (rank > 3) for this mode.")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Write full results as JSON.")
	return cmd
}
