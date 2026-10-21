// Package config reads and writes .docgraph/config.yaml.
//
// Missing keys keep their defaults, so a partial file is valid. Lists replace
// the default list entirely.
package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const (
	Dir        = ".docgraph"
	ConfigFile = "config.yaml"
	DBFile     = "docgraph.db"
)

type Index struct {
	TargetTokens     int      `yaml:"target_tokens"`
	MaxTokens        int      `yaml:"max_tokens"`
	OverlapTokens    int      `yaml:"overlap_tokens"`
	MinSectionTokens int      `yaml:"min_section_tokens"`
	Extensions       []string `yaml:"extensions"`
	Ignore           []string `yaml:"ignore"`
	ExcludeDirs      []string `yaml:"exclude_dirs"`
}

type Embedding struct {
	Model         string  `yaml:"model"`
	Backend       string  `yaml:"backend"`
	BatchSize     int     `yaml:"batch_size"`
	QueryPrefix   *string `yaml:"query_prefix"`   // nil = derived from model (e5: "query: ")
	PassagePrefix *string `yaml:"passage_prefix"` // nil = derived from model (e5: "passage: ")
}

type BM25 struct {
	HeadingWeight     float64 `yaml:"heading_weight"`
	HeadingPathWeight float64 `yaml:"heading_path_weight"`
	ContentWeight     float64 `yaml:"content_weight"`
	PrefixLen         int     `yaml:"prefix_len"`
	MinTermLen        int     `yaml:"min_term_len"`
}

type RRF struct {
	K            int     `yaml:"k"`
	BM25Weight   float64 `yaml:"bm25_weight"`
	VectorWeight float64 `yaml:"vector_weight"`
}

type Search struct {
	Mode             string `yaml:"mode"`
	TopK             int    `yaml:"top_k"`
	BM25Candidates   int    `yaml:"bm25_candidates"`
	VectorCandidates int    `yaml:"vector_candidates"`
	BM25             BM25   `yaml:"bm25"`
	RRF              RRF    `yaml:"rrf"`
}

type Config struct {
	Index     Index     `yaml:"index"`
	Embedding Embedding `yaml:"embedding"`
	Search    Search    `yaml:"search"`
}

// AgentIgnore lists AI coding-agent instruction files and folders: they
// describe how to work in a repository, not what the documentation says, and
// would crowd the search results. gitignore syntax, matched at any depth; a
// "!CLAUDE.md" line in .docgraphignore re-includes a file.
var AgentIgnore = []string{
	"CLAUDE.md", "CLAUDE.local.md", "AGENTS.md", "AGENT.md", "GEMINI.md",
	".claude/", ".cursor/", ".cursorrules", ".windsurf/", ".windsurfrules",
	".clinerules", ".clinerules/", ".roo/", ".kiro/", ".junie/", ".codex/", ".gemini/", ".continue/",
	".github/copilot-instructions.md", ".github/instructions/", ".github/prompts/", ".github/chatmodes/",
}

func Default() Config {
	return Config{
		Index: Index{
			TargetTokens: 400, MaxTokens: 480, OverlapTokens: 60,
			Extensions:  []string{".md", ".markdown", ".puml", ".plantuml", ".mmd", ".mermaid", ".txt"},
			Ignore:      append([]string{}, AgentIgnore...),
			ExcludeDirs: []string{".git", ".docgraph", ".venv", "node_modules", "__pycache__"},
		},
		Embedding: Embedding{Model: "intfloat/multilingual-e5-base", Backend: "onnx-fp16", BatchSize: 32},
		Search: Search{
			Mode: "hybrid", TopK: 10, BM25Candidates: 30, VectorCandidates: 30,
			BM25: BM25{HeadingWeight: 5, HeadingPathWeight: 3, ContentWeight: 1, MinTermLen: 1},
			RRF:  RRF{K: 60, BM25Weight: 1, VectorWeight: 1},
		},
	}
}

func ProjectDir(root string) string { return filepath.Join(root, Dir) }
func DBPath(root string) string     { return filepath.Join(root, Dir, DBFile) }

// Load returns the defaults overlaid with root/.docgraph/config.yaml, if present.
func Load(root string) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(filepath.Join(ProjectDir(root), ConfigFile))
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	err = yaml.Unmarshal(data, &cfg)
	return cfg, err
}

func Save(root string, cfg Config) (string, error) {
	path := filepath.Join(ProjectDir(root), ConfigFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return "", err
	}
	return path, os.WriteFile(path, data, 0o644)
}

// FindRoot walks up from start until it finds a directory containing .docgraph/ (like git).
func FindRoot(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if st, err := os.Stat(ProjectDir(dir)); err == nil && st.IsDir() {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("no .docgraph/ found here or in any parent; run `docgraph init`")
		}
		dir = parent
	}
}
