package convert

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/skromesch/docgraph/internal/testutil"
)

func convertMD(t *testing.T, md string) string {
	t.Helper()
	dir := t.TempDir()
	file := testutil.Docx(t, dir, "x.docx", md)
	pandoc, err := FindPandoc("")
	if err != nil {
		t.Fatal(err)
	}
	out, err := Docx(pandoc, file)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestDocxHeadingsAndTable(t *testing.T) {
	out := convertMD(t, "# Spec\n\n## Mezők\n\n| Név | Típus |\n|---|---|\n| ügyfélAzonosító | int |\n")
	squashed := regexp.MustCompile(` +`).ReplaceAllString(out, " ") // pandoc pads table columns
	for _, want := range []string{"# Spec\n", "## Mezők\n", "| ügyfélAzonosító | int |"} {
		if !strings.Contains(squashed, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestDocxMergedCellTextSearchable(t *testing.T) {
	md := "+-------+-------+\n| A     | B     |\n+=======+=======+\n| összevontCella|\n+-------+-------+\n| c     | d     |\n+-------+-------+\n"
	if out := convertMD(t, md); !strings.Contains(out, "összevontCella") {
		t.Errorf("merged cell text lost:\n%s", out)
	}
}

func TestDocxImageAltOnly(t *testing.T) {
	png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==")
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.png"), png, 0o644)
	file := testutil.Docx(t, dir, "x.docx", "Szöveg előtte.\n\n![Architektúra ábra](a.png)\n")
	pandoc, _ := FindPandoc("")
	out, err := Docx(pandoc, file)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Architektúra ábra") || strings.Contains(out, "](") || strings.Contains(out, "<img") {
		t.Errorf("want alt text only:\n%s", out)
	}
}

func TestDocxCorruptFails(t *testing.T) {
	pandoc, err := FindPandoc("")
	if err != nil {
		t.Skip("pandoc not installed")
	}
	bad := filepath.Join(t.TempDir(), "bad.docx")
	os.WriteFile(bad, []byte("not a zip"), 0o644)
	if _, err := Docx(pandoc, bad); err == nil {
		t.Fatal("want error")
	}
}

func TestFindPandocConfigPath(t *testing.T) {
	if _, err := FindPandoc(filepath.Join(t.TempDir(), "nope")); !errors.Is(err, ErrNoPandoc) {
		t.Fatalf("got %v", err)
	}
}

func TestWarningText(t *testing.T) {
	w := Warning(3)
	if !strings.HasPrefix(w, "warning: 3 .docx file(s) skipped: pandoc not found. Install it (") ||
		!strings.HasSuffix(w, "or set convert.pandoc_path in .docgraph/config.yaml") {
		t.Errorf("got %q", w)
	}
}

func TestPaths(t *testing.T) {
	if got := Dir("/p/.docgraph/docgraph.db"); got != filepath.FromSlash("/p/.docgraph/converted") {
		t.Errorf("Dir: %s", got)
	}
	if got := Path("/c", "specs/Foo.docx"); got != filepath.FromSlash("/c/specs/Foo.docx.md") {
		t.Errorf("Path: %s", got)
	}
	if !IsDocx("a/B.DOCX") || IsDocx("a.md") {
		t.Error("IsDocx")
	}
}
