// Package convert turns Word documents into hidden Markdown with pandoc, so the
// Markdown parser can index them. The converted files live under
// .docgraph/converted/ and are never part of the documentation folder.
package convert

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// Timeout bounds one pandoc run.
const Timeout = 60 * time.Second

var ErrNoPandoc = errors.New("pandoc not found")

// FindPandoc returns cfgPath when it names an existing file, otherwise pandoc
// from PATH. An empty cfgPath means PATH only.
func FindPandoc(cfgPath string) (string, error) {
	if cfgPath != "" {
		if st, err := os.Stat(cfgPath); err != nil || st.IsDir() {
			return "", fmt.Errorf("%w at %s", ErrNoPandoc, cfgPath)
		}
		return cfgPath, nil
	}
	p, err := exec.LookPath("pandoc")
	if err != nil {
		return "", ErrNoPandoc
	}
	return p, nil
}

var (
	imageRE = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)(\{[^}]*\})?`)
	imgTag  = regexp.MustCompile(`<img\b[^>]*?(?:alt="([^"]*)")?[^>]*>`)
)

// Docx converts a .docx file to GitHub-flavored Markdown with LF line ends.
// Images keep only their alt text (an image without one is dropped).
func Docx(pandoc, file string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	var stderr strings.Builder
	cmd := exec.CommandContext(ctx, pandoc, "-f", "docx", "-t", "gfm", "--wrap=none", file)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg, _, _ := strings.Cut(strings.TrimSpace(stderr.String()), "\n")
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("pandoc: %s", msg)
	}
	text := strings.ReplaceAll(string(out), "\r\n", "\n")
	text = imageRE.ReplaceAllString(text, "$1")
	return imgTag.ReplaceAllString(text, "$1"), nil
}

// IsDocx reports whether a document path is a Word document.
func IsDocx(rel string) bool { return strings.EqualFold(filepath.Ext(rel), ".docx") }

// Dir is the converted-files folder next to the database.
func Dir(dbPath string) string { return filepath.Join(filepath.Dir(dbPath), "converted") }

// Path is the hidden Markdown file for a document path.
func Path(dir, rel string) string { return filepath.Join(dir, filepath.FromSlash(rel)+".md") }

// InstallHint is the install command for this OS.
func InstallHint() string {
	switch runtime.GOOS {
	case "darwin":
		return "macOS: brew install pandoc"
	case "linux":
		return "Debian/Ubuntu: sudo apt install pandoc"
	}
	return "see https://pandoc.org/installing.html"
}

// Warning is the one-line notice for n skipped Word documents.
func Warning(n int) string {
	return fmt.Sprintf("warning: %d .docx file(s) skipped: pandoc not found. Install it (%s) or set convert.pandoc_path in .docgraph/config.yaml",
		n, InstallHint())
}

// Write stores converted text atomically (temp file + rename).
func Write(path, text string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	_, werr := tmp.WriteString(text)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(tmp.Name())
		return werr
	}
	return os.Rename(tmp.Name(), path)
}
