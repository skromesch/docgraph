package service

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/text/unicode/norm"

	"github.com/skromesch/docgraph/internal/pyutil"
)

const (
	MaxGrepResults = 200
	MaxGrepFiles   = 300
	GrepLineChars  = 200
)

// Grep is the line search tool: substrings (word parts, inflected forms) and exact
// identifiers that BM25's whole-word terms miss. Literal unless regex is set.
// With filesOnly, every matching file is listed with its match count instead of the lines.
func (s *Service) Grep(pattern string, regex bool, glob string, ignoreCase bool, maxResults int, filesOnly bool) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maybeSync()
	return s.grep(pattern, regex, glob, ignoreCase, maxResults, filesOnly)
}

func (s *Service) grep(pattern string, regex bool, glob string, ignoreCase bool, maxResults int, filesOnly bool) string {
	pattern = norm.NFC.String(pattern)
	if pyutil.Strip(pattern) == "" {
		return "Empty pattern."
	}
	maxResults = max(1, min(maxResults, MaxGrepResults))
	var match func(string) bool
	switch {
	case regex:
		expr := PyRegex(pattern)
		if ignoreCase {
			expr = "(?i)" + expr
		}
		rx, err := regexp.Compile(expr)
		if err != nil {
			return "Invalid regex: " + err.Error()
		}
		match = rx.MatchString
	case ignoreCase:
		needle := pyutil.Lower(pattern)
		match = func(l string) bool { return strings.Contains(pyutil.Lower(l), needle) }
	default:
		match = func(l string) bool { return strings.Contains(l, pattern) }
	}
	g := norm.NFC.String(glob)
	if g == "" {
		g = "*"
	}
	docs, err := s.DB.Documents()
	if err != nil {
		return "Grep failed: " + err.Error()
	}
	var paths []string
	for p := range docs {
		if fnmatch(g, p) || fnmatch(g+"/*", p) {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths) // UTF-8 byte order = Python's code point order
	var groups []string
	total, files := 0, 0
	for _, p := range paths {
		full := s.fileFor(p)
		if full == "" {
			continue
		}
		raw, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		type hit struct {
			n    int
			line string
		}
		var hits []hit
		for i, l := range pyutil.SplitLines(norm.NFC.String(pyutil.DecodeFile(raw))) {
			if match(l) {
				hits = append(hits, hit{i + 1, l})
			}
		}
		if len(hits) == 0 {
			continue
		}
		files++
		if filesOnly {
			total += len(hits)
			if files <= MaxGrepFiles {
				groups = append(groups, fmt.Sprintf("%s (%s)", p, matches(len(hits))))
			}
			continue
		}
		room := maxResults - total
		total += len(hits)
		if room <= 0 {
			continue
		}
		rows := make([]string, 0, min(room, len(hits)))
		for _, h := range hits[:min(room, len(hits))] {
			l := pyutil.Strip(h.line)
			if r := []rune(l); len(r) > GrepLineChars {
				l = string(r[:GrepLineChars]) + "…"
			}
			rows = append(rows, fmt.Sprintf("  %d: %s", h.n, l))
		}
		groups = append(groups, fmt.Sprintf("%s (%s)\n", p, matches(len(hits)))+strings.Join(rows, "\n"))
	}
	if len(groups) == 0 {
		return fmt.Sprintf("No lines match '%s'.", pattern)
	}
	plural := "s"
	if files == 1 {
		plural = ""
	}
	if filesOnly {
		more := ""
		if files > MaxGrepFiles {
			more = fmt.Sprintf("; showing the first %d files", MaxGrepFiles)
		}
		return strings.Join(groups, "\n") + fmt.Sprintf("\n\n[%s in %d file%s%s]", matches(total), files, plural, more)
	}
	footer := fmt.Sprintf("%s in %d file%s", matches(total), files, plural)
	if total > maxResults {
		footer += fmt.Sprintf("; showing the first %d. Narrow with path_glob or a more specific pattern", maxResults)
	}
	return strings.Join(groups, "\n\n") + "\n\n[" + footer + "]"
}

func matches(n int) string {
	if n == 1 {
		return "1 match"
	}
	return fmt.Sprintf("%d matches", n)
}

// PyRegex makes the common Python re escapes Unicode-aware for RE2, as the agent
// writes Python-style patterns: \w \W \d \D \s \S. Inside a class only the
// positive forms are rewritten. Other syntax goes to RE2 unchanged.
func PyRegex(p string) string {
	var b strings.Builder
	inClass := false
	for i := 0; i < len(p); i++ {
		c := p[i]
		if c == '\\' && i+1 < len(p) {
			e := p[i+1]
			i++
			switch {
			case e == 'w' && inClass:
				b.WriteString(pyutil.Word)
			case e == 'w':
				b.WriteString("[" + pyutil.Word + "]")
			case e == 'W' && !inClass:
				b.WriteString("[^" + pyutil.Word + "]")
			case e == 'd':
				b.WriteString(`\p{Nd}`)
			case e == 'D' && !inClass:
				b.WriteString(`\P{Nd}`)
			case e == 's' && inClass:
				b.WriteString(pyutil.Space)
			case e == 's':
				b.WriteString("[" + pyutil.Space + "]")
			case e == 'S' && !inClass:
				b.WriteString("[^" + pyutil.Space + "]")
			default:
				b.WriteByte('\\')
				b.WriteByte(e)
			}
			continue
		}
		switch {
		case c == '[' && !inClass:
			inClass = true
			b.WriteByte(c)
			// a leading ']' (or '^]') is a literal member
			if i+1 < len(p) && p[i+1] == '^' {
				b.WriteByte('^')
				i++
			}
			if i+1 < len(p) && p[i+1] == ']' {
				b.WriteByte(']')
				i++
			}
			continue
		case c == ']' && inClass:
			inClass = false
		}
		b.WriteByte(c)
	}
	return b.String()
}
