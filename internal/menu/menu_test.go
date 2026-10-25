package menu

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/skromesch/docgraph/internal/config"
	"github.com/skromesch/docgraph/internal/embed"
	"github.com/skromesch/docgraph/internal/store"
)

func copyTree(t *testing.T, src, dst string) {
	filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if info.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		data, _ := os.ReadFile(p)
		return os.WriteFile(filepath.Join(dst, rel), data, 0o644)
	})
}

// Plain mode end to end: init -> index (keyword only) -> add an ignore rule -> quit.
func TestPlainMenuFlow(t *testing.T) {
	fixtures, _ := filepath.Abs("../../testdata/fixtures/wiki")
	dir := t.TempDir()
	copyTree(t, fixtures, filepath.Join(dir, "wiki"))
	t.Setenv("DOCGRAPH_MODEL_DIR", t.TempDir()) // no model: the menu offers a download, we decline
	t.Setenv("DOCGRAPH_LIB_DIR", t.TempDir())
	t.Chdir(dir)

	input := strings.Join([]string{
		"1",     // Initialize a project here
		"y",     // create .docgraph here?
		"wiki",  // docs folder
		"1",     // ignore editor: Continue (no rules yet)
		"n",     // download the model? no -> keyword only
		"2",     // Ignore rules
		"1",     // Add a rule
		"*.txt", // pattern
		"y",     // save
		"n",     // sync now? no
		"4",     // back
		"8",     // Quit
	}, "\n") + "\n"
	var out, errOut bytes.Buffer
	deps := Deps{NewEmbedder: func(config.Config) (embed.Embedder, error) { return nil, errors.New("unused") }, Exe: "/x/docgraph"}
	code := Run(strings.NewReader(input), &out, &errOut, true, deps)
	if code != 0 {
		t.Fatalf("exit %d\nstderr: %s\nout: %s", code, errOut.String(), out.String())
	}
	o := out.String()
	for _, want := range []string{"initialized", "6 documents: 6 new", "With this change: 5 files indexed, 1 excluded", "- notes.txt", "✓ saved"} {
		if !strings.Contains(o, want) {
			t.Errorf("output lacks %q\n%s", want, o)
		}
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "wiki", ".docgraphignore")); string(data) != "*.txt\n" {
		t.Errorf(".docgraphignore = %q", data)
	}
	db, err := store.Open(config.DBPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if s, _ := db.Stats(); s.Documents != 6 || s.ChunksEmbedded != 0 {
		t.Errorf("stats %+v", s) // sync was declined: notes.txt still indexed, nothing embedded
	}
}

func TestPipedInputEndingEarlyStops(t *testing.T) {
	t.Chdir(t.TempDir())
	var out, errOut bytes.Buffer
	code := Run(strings.NewReader("2\n"), &out, &errOut, true, Deps{})
	if code == 0 || !strings.Contains(errOut.String(), "input ended") {
		t.Fatalf("code %d, stderr %q", code, errOut.String())
	}
}

// Rules added after choosing the folder apply to the very first index.
func TestPlainSetupIgnoreBeforeIndex(t *testing.T) {
	fixtures, _ := filepath.Abs("../../testdata/fixtures/wiki")
	dir := t.TempDir()
	copyTree(t, fixtures, filepath.Join(dir, "wiki"))
	t.Setenv("DOCGRAPH_MODEL_DIR", t.TempDir())
	t.Setenv("DOCGRAPH_LIB_DIR", t.TempDir())
	t.Chdir(dir)
	input := strings.Join([]string{"1", "y", "wiki",
		"2",     // Add a rule (Continue is first)
		"*.txt", // pattern
		"y",     // save
		"1",     // Continue: index 5 files
		"n",     // no model
		"8",     // Quit
	}, "\n") + "\n"
	var out, errOut bytes.Buffer
	deps := Deps{NewEmbedder: func(config.Config) (embed.Embedder, error) { return nil, errors.New("unused") }, Exe: "/x/docgraph"}
	if code := Run(strings.NewReader(input), &out, &errOut, true, deps); code != 0 {
		t.Fatalf("exit %d\nstderr: %s\nout: %s", code, errOut.String(), out.String())
	}
	if o := out.String(); !strings.Contains(o, "Continue: index 5 files") || !strings.Contains(o, "5 documents: 5 new") {
		t.Errorf("output:\n%s", o)
	}
}

// The pandoc entry saves convert.pandoc_path; the header shows what was found.
func TestPlainPandocPath(t *testing.T) {
	fixtures, _ := filepath.Abs("../../testdata/fixtures/wiki")
	dir := t.TempDir()
	copyTree(t, fixtures, filepath.Join(dir, "wiki"))
	t.Setenv("DOCGRAPH_MODEL_DIR", t.TempDir())
	t.Setenv("DOCGRAPH_LIB_DIR", t.TempDir())
	t.Setenv("PATH", t.TempDir()) // no pandoc on PATH
	fake := filepath.Join(dir, "bin", "pandoc")
	os.MkdirAll(filepath.Dir(fake), 0o755)
	os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o755)
	t.Chdir(dir)
	input := strings.Join([]string{"1", "y", "wiki", "1", "n",
		"6",  // Pandoc (Word .docx support)
		fake, // path
		"8",  // Quit
	}, "\n") + "\n"
	var out, errOut bytes.Buffer
	deps := Deps{NewEmbedder: func(config.Config) (embed.Embedder, error) { return nil, errors.New("unused") }, Exe: "/x/docgraph"}
	if code := Run(strings.NewReader(input), &out, &errOut, true, deps); code != 0 {
		t.Fatalf("exit %d\nstderr: %s\nout: %s", code, errOut.String(), out.String())
	}
	o := out.String()
	if !strings.Contains(o, "Pandoc: ✗ not found") || !strings.Contains(o, "Pandoc: ✓ "+fake) {
		t.Errorf("output:\n%s", o)
	}
	if cfg, _ := config.Load(dir); cfg.Convert.PandocPath != fake {
		t.Errorf("pandoc_path = %q", cfg.Convert.PandocPath)
	}
}
