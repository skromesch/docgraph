package index

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ReadIgnoreFile returns the lines of <root>/.docgraphignore (nil if absent).
func ReadIgnoreFile(root string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(root, IgnoreFile))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	text := strings.TrimPrefix(strings.ReplaceAll(string(data), "\r\n", "\n"), "\ufeff")
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil, nil
	}
	return lines, nil
}

// WriteIgnoreFile replaces <root>/.docgraphignore; an empty list removes the file.
func WriteIgnoreFile(root string, lines []string) error {
	p := filepath.Join(root, IgnoreFile)
	if len(lines) == 0 {
		err := os.Remove(p)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

// Patterns returns the active pattern lines (no comments, no blanks).
func Patterns(lines []string) []string {
	var out []string
	for _, l := range lines {
		if t := strings.TrimSpace(l); t != "" && !strings.HasPrefix(t, "#") {
			out = append(out, l)
		}
	}
	return out
}

// PreviewIgnore scans root with the config patterns plus fileLines (instead of
// the file on disk) and returns the files that the patterns exclude, sorted.
func PreviewIgnore(root string, extensions, excludeDirs, configPatterns, fileLines []string) (kept int, excluded []string, err error) {
	all, err := scanWith(root, extensions, excludeDirs, nil)
	if err != nil {
		return 0, nil, err
	}
	with, err := scanWith(root, extensions, excludeDirs, append(append([]string{}, configPatterns...), fileLines...))
	if err != nil {
		return 0, nil, err
	}
	for p := range all {
		if _, ok := with[p]; !ok {
			excluded = append(excluded, p)
		}
	}
	sort.Strings(excluded)
	return len(with), excluded, nil
}
