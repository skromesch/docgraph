package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/skromesch/docgraph/internal/chunk"
	"github.com/skromesch/docgraph/internal/config"
	"github.com/skromesch/docgraph/internal/store"
)

func TestUnicodeRegex(t *testing.T) {
	for in, want := range map[string]string{
		`\w+`:     `[\p{L}\p{N}_]+`,
		`[\w-]+`:  `[\p{L}\p{N}_-]+`,
		`\W`:      `[^\p{L}\p{N}_]`,
		`\d\D`:    `\p{Nd}\P{Nd}`,
		`a\.b\\w`: `a\.b\\w`,
		`[]\w]`:   `[]\p{L}\p{N}_]`,
		`[^]x]\w`: `[^]x][\p{L}\p{N}_]`,
	} {
		if got := UnicodeRegex(in); got != want {
			t.Errorf("UnicodeRegex(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGrepFixture(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "A.md"), []byte("# Résumé\n\nThe naïve localization works.\n"+strings.Repeat("x", 300)+"\n"), 0o644)
	db, _ := store.Open(filepath.Join(t.TempDir(), "t.db"))
	defer db.Close()
	svc := New(db, config.Default(), root)
	svc.syncPeriod = 0
	svc.EnableAutoSync(chunk.Heuristic)
	if out := svc.Grep("NAÏVE LOCALIZ", false, "", true, 50, false); out != "A.md (1 match)\n  3: The naïve localization works.\n\n[1 match in 1 file]" {
		t.Fatalf("literal: %q", out)
	}
	if out := svc.Grep(`^#\s+\w+$`, true, "", true, 50, false); !strings.HasPrefix(out, "A.md (1 match)\n  1: # Résumé") {
		t.Fatalf("unicode regex: %q", out)
	}
	if out := svc.Grep("xxx", false, "", true, 50, false); !strings.Contains(out, strings.Repeat("x", 200)+"…\n") {
		t.Fatalf("long line not cut: %q", out)
	}
	if out := svc.Grep("(", true, "", true, 50, false); !strings.HasPrefix(out, "Invalid regex") {
		t.Fatalf("bad regex: %q", out)
	}
}
