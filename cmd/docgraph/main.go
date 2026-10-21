// Command docgraph: local documentation indexing and hybrid search.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	"github.com/skromesch/docgraph/internal/config"
	"github.com/skromesch/docgraph/internal/menu"
	"github.com/skromesch/docgraph/internal/store"
)

func main() {
	root := &cobra.Command{
		Use: "docgraph", Short: "DocGraph: local documentation search.", SilenceUsage: true, Version: version,
		// no arguments: the interactive menu for a human at a terminal, help otherwise
		RunE: func(cmd *cobra.Command, args []string) error {
			if !menu.StdinIsTerminal() {
				return cmd.Help()
			}
			if code := menu.Run(os.Stdin, os.Stdout, os.Stderr, false, menuDeps()); code != 0 {
				os.Exit(code)
			}
			return nil
		},
	}
	root.AddCommand(initCmd(), statsCmd(), indexCmd(), syncCmd(), searchCmd(), mcpCmd(), evalCmd(),
		menuCmd(), modelCmd())
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

// openProject finds .docgraph/ in the working directory or a parent.
func openProject() (string, config.Config, *store.DB, error) {
	root, err := config.FindRoot(".")
	if err != nil {
		return "", config.Config{}, nil, err
	}
	cfg, err := config.Load(root)
	if err != nil {
		return "", cfg, nil, err
	}
	db, err := store.Open(config.DBPath(root))
	return root, cfg, db, err
}

func initCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create .docgraph/config.yaml and docgraph.db in the current directory.",
		RunE: func(cmd *cobra.Command, args []string) error {
			root, _ := filepath.Abs(".")
			cfgPath := filepath.Join(config.ProjectDir(root), config.ConfigFile)
			if _, err := os.Stat(cfgPath); err == nil && !force {
				fmt.Printf("%s already exists (use --force to overwrite)\n", cfgPath)
			} else if _, err := config.Save(root, config.Default()); err != nil {
				return err
			}
			db, err := store.Open(config.DBPath(root))
			if err != nil {
				return err
			}
			defer db.Close()
			fmt.Printf("Initialized %s\n", config.ProjectDir(root))
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Overwrite an existing config.yaml.")
	return cmd
}

func statsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stats",
		Short: "Show index statistics.",
		RunE: func(cmd *cobra.Command, args []string) error {
			root, _, db, err := openProject()
			if err != nil {
				return err
			}
			defer db.Close()
			s, err := db.Stats()
			if err != nil {
				return err
			}
			types := make([]string, 0, len(s.DocumentsByType))
			for t := range s.DocumentsByType {
				types = append(types, t)
			}
			sort.Strings(types)
			fmt.Printf("Project: %s\nsource_root: %s\ndocuments: %d\n", root, s.SourceRoot, s.Documents)
			for _, t := range types {
				fmt.Printf("  %s: %d\n", t, s.DocumentsByType[t])
			}
			fmt.Printf("chunks: %d\nchunks_embedded: %d\navg_chunk_tokens: %.1f\nmax_chunk_tokens: %d\n"+
				"embedding_cache: %d\nembedding_model: %s\nembedding_dim: %s\nschema_version: %s\n",
				s.Chunks, s.ChunksEmbedded, s.AvgChunkTokens, s.MaxChunkTokens, s.EmbeddingCache,
				s.EmbeddingModel, s.EmbeddingDim, s.SchemaVersion)
			return nil
		},
	}
}
