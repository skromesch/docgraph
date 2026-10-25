package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Docx writes dir/name as a Word document built by pandoc from Markdown,
// skipping the test when pandoc is not installed.
func Docx(t testing.TB, dir, name, markdown string) string {
	t.Helper()
	pandoc, err := exec.LookPath("pandoc")
	if err != nil {
		t.Skip("pandoc not installed")
	}
	out := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(pandoc, "-f", "markdown", "-o", out)
	cmd.Dir = dir // relative image paths resolve against dir
	cmd.Stdin = strings.NewReader(markdown)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pandoc: %v: %s", err, b)
	}
	return out
}
