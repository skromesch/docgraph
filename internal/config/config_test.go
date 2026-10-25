package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPartialKeepsDefaults(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, Dir), 0o755)
	os.WriteFile(filepath.Join(root, Dir, ConfigFile), []byte(
		"index:\n  target_tokens: 300\nembedding:\n  device: null\nsearch:\n  rrf:\n    k: 20\n  rerank:\n    model: x\n"), 0o644)
	cfg, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Index.TargetTokens != 300 || cfg.Index.MaxTokens != 480 {
		t.Errorf("index: %+v", cfg.Index)
	}
	if cfg.Search.RRF.K != 20 || cfg.Search.RRF.BM25Weight != 1 || cfg.Search.BM25.HeadingWeight != 5 {
		t.Errorf("search: %+v", cfg.Search)
	}
	if cfg.Embedding.Model != "intfloat/multilingual-e5-base" {
		t.Errorf("embedding: %+v", cfg.Embedding)
	}
}

func TestFindRoot(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, Dir), 0o755)
	sub := filepath.Join(root, "a", "b")
	os.MkdirAll(sub, 0o755)
	got, err := FindRoot(sub)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(root)
	if g, _ := filepath.EvalSymlinks(got); g != want {
		t.Errorf("got %s want %s", got, root)
	}
}

func TestDocxDefaultsAndPandocPath(t *testing.T) {
	found := false
	for _, e := range Default().Index.Extensions {
		found = found || e == ".docx"
	}
	if !found {
		t.Error(".docx not in default extensions")
	}
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, Dir), 0o755)
	os.WriteFile(filepath.Join(root, Dir, ConfigFile), []byte("convert:\n  pandoc_path: /x\n"), 0o644)
	cfg, err := Load(root)
	if err != nil || cfg.Convert.PandocPath != "/x" {
		t.Errorf("got %+v %v", cfg.Convert, err)
	}
}
