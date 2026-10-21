package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/skromesch/docgraph/internal/config"
	"github.com/skromesch/docgraph/internal/embed"
	"github.com/skromesch/docgraph/internal/menu"
	"github.com/skromesch/docgraph/internal/setup"
)

func menuDeps() menu.Deps {
	exe, _ := os.Executable()
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	return menu.Deps{NewEmbedder: newEmbedder, Exe: exe}
}

func menuCmd() *cobra.Command {
	var plain bool
	cmd := &cobra.Command{
		Use:     "configure",
		Aliases: []string{"menu"},
		Short:   "Interactive menu: init, index, sync, ignore rules, model download, MCP registration.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if code := menu.Run(os.Stdin, os.Stdout, os.Stderr, plain, menuDeps()); code != 0 {
				os.Exit(code)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&plain, "plain", false, "Plain prompts (no TUI); automatic when stdin is not a terminal.")
	return cmd
}

// loadConfigOrDefault: the project's config if there is one, else the defaults.
func loadConfigOrDefault() config.Config {
	if root, err := config.FindRoot("."); err == nil {
		if cfg, err := config.Load(root); err == nil {
			return cfg
		}
	}
	return config.Default()
}

func modelCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "model", Short: "Embedding model and ONNX Runtime: status / pull."}
	cmd.AddCommand(&cobra.Command{
		Use:   "pull",
		Short: "Download the configured model and the ONNX Runtime (pinned, SHA-256 verified).",
		RunE: func(cmd *cobra.Command, args []string) error {
			return menu.Pull(loadConfigOrDefault(), os.Stdout)
		},
	}, &cobra.Command{
		Use:   "status",
		Short: "Show where the model and the ONNX Runtime are, or that they are missing.",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfigOrDefault()
			mdl, ok := setup.FindModel(cfg.Embedding.Model, cfg.Embedding.Backend)
			fmt.Printf("model:   %s (%s)\n", cfg.Embedding.Model, cfg.Embedding.Backend)
			switch {
			case !ok:
				fmt.Printf("         no download available; expected in %s\n", embed.ModelDir(cfg.Embedding.Model))
			case setup.ModelInstalled(mdl):
				fmt.Printf("         ✓ %s\n", embed.ModelDir(mdl.Name))
			default:
				fmt.Printf("         ✗ missing (docgraph model pull)\n")
			}
			if lib, err := embed.FindORTLibrary(); err == nil {
				fmt.Printf("runtime: ✓ %s\n", lib)
			} else {
				fmt.Printf("runtime: ✗ %v\n", err)
			}
			return nil
		},
	})
	return cmd
}
