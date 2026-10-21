// Package index is the indexing pipeline: scan -> diff by hash -> parse ->
// chunk -> store (-> embed).
package index

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
	"golang.org/x/text/unicode/norm"

	"github.com/skromesch/docgraph/internal/parse"
)

const IgnoreFile = ".docgraphignore"

// Ignore matches gitignore-style patterns: config patterns first, then
// <root>/.docgraphignore; the last matching pattern wins.
type Ignore struct{ m gitignore.Matcher }

func LoadIgnore(root string, patterns []string) Ignore {
	lines := append([]string{}, patterns...)
	if data, err := os.ReadFile(filepath.Join(root, IgnoreFile)); err == nil {
		text := strings.TrimPrefix(string(data), "\ufeff")
		lines = append(lines, strings.Split(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n"), "\n")...)
	}
	return NewIgnore(lines)
}

// NewIgnore builds a matcher from pattern lines (comments and blanks are skipped).
func NewIgnore(lines []string) Ignore {
	var ps []gitignore.Pattern
	for _, l := range lines {
		l = norm.NFC.String(l)
		if t := strings.TrimSpace(l); t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		ps = append(ps, gitignore.ParsePattern(l, nil))
	}
	return Ignore{gitignore.NewMatcher(ps)}
}

// Match reports whether rel (NFC, "/"-separated, relative to the root) is ignored.
func (ig Ignore) Match(rel string, isDir bool) bool {
	return ig.m.Match(strings.Split(rel, "/"), isDir)
}

// Scan returns {document path: file on disk} for supported, non-ignored files.
// Document paths are relative POSIX paths in Unicode NFC (macOS reports NFD).
// Ignored directories are pruned, so their files cannot be re-included (as in git).
func Scan(root string, extensions, excludeDirs, ignore []string) (map[string]string, error) {
	return scanIgnore(root, extensions, excludeDirs, LoadIgnore(root, ignore))
}

// scanWith scans with exactly these pattern lines (the file on disk is not read).
func scanWith(root string, extensions, excludeDirs, lines []string) (map[string]string, error) {
	return scanIgnore(root, extensions, excludeDirs, NewIgnore(lines))
}

func scanIgnore(root string, extensions, excludeDirs []string, ig Ignore) (map[string]string, error) {
	exts := map[string]bool{}
	for _, e := range extensions {
		exts[strings.ToLower(e)] = true
	}
	excluded := map[string]bool{}
	for _, d := range excludeDirs {
		excluded[norm.NFC.String(d)] = true
	}
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entry: skip, like os.walk
		}
		if p == root {
			return nil
		}
		relOS, _ := filepath.Rel(root, p)
		rel := norm.NFC.String(filepath.ToSlash(relOS))
		if d.IsDir() {
			if excluded[norm.NFC.String(d.Name())] || ig.Match(rel, true) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() && d.Type()&fs.ModeSymlink == 0 {
			return nil
		}
		if exts[strings.ToLower(parse.Suffix(d.Name()))] && !ig.Match(rel, false) {
			out[rel] = p
		}
		return nil
	})
	return out, err
}
