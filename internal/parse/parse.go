// Package parse turns a file's text into a flat list of sections.
// See docs/CHUNKING.md.
package parse

import (
	"path"
	"regexp"
	"strings"

	"golang.org/x/text/unicode/norm"

	"github.com/skromesch/docgraph/internal/textutil"
)

// Section is a contiguous line range with a heading hierarchy.
type Section struct {
	Heading   string
	Level     int      // 0 = document root / no heading
	Path      []string // full heading hierarchy, including Heading
	StartLine int      // 1-based, inclusive
	EndLine   int      // 1-based, inclusive
	Lines     []string // raw lines StartLine..EndLine
	HasBody   bool     // false when the section is only a heading line
}

type Document struct {
	Title    string
	Type     string
	Sections []Section
}

type Parser interface {
	Type() string
	Extensions() []string
	Parse(path, text string) Document
}

var registry = map[string]Parser{}

func register(p Parser) {
	for _, ext := range p.Extensions() {
		registry[ext] = p
	}
}

func init() {
	register(Markdown{})
	register(PlantUML{})
	register(Mermaid{})
	register(Text{})
	register(Docx{})
}

// For returns the parser for a path by its (lower-cased) suffix, or nil.
func For(p string) Parser { return registry[strings.ToLower(Suffix(p))] }

// Suffix mirrors PurePosixPath(p).suffix.
func Suffix(p string) string {
	name := path.Base(p)
	if i := strings.LastIndex(name, "."); i > 0 && i < len(name)-1 {
		return name[i:]
	}
	return ""
}

// Stem mirrors PurePosixPath(p).stem.
func Stem(p string) string {
	name := path.Base(p)
	if s := Suffix(name); s != "" {
		return name[:len(name)-len(s)]
	}
	return name
}

// TitleFromPath: Azure DevOps wiki convention, file name is the page title,
// '-' = space, '%2D' = '-'.
func TitleFromPath(p string) string {
	stem := norm.NFC.String(Stem(p))
	t := norm.NFC.String(textutil.Strip(textutil.Unquote(strings.ReplaceAll(stem, "-", " "))))
	if t == "" {
		return stem
	}
	return t
}

var lineBreak = regexp.MustCompile(`\r\n|\r|\n`)

// SplitLines: trailing newlines are removed first; a blank text has no lines.
func SplitLines(text string) []string {
	if textutil.Strip(text) == "" {
		return nil
	}
	return lineBreak.Split(strings.TrimRight(text, "\r\n"), -1)
}

func anyNonBlank(lines []string) bool {
	for _, l := range lines {
		if textutil.Strip(l) != "" {
			return true
		}
	}
	return false
}
