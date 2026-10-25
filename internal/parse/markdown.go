package parse

import (
	"regexp"
	"strings"

	"github.com/skromesch/docgraph/internal/textutil"
)

var (
	atxRE    = regexp.MustCompile(`^ {0,3}(#{1,6})[ \t]+(.*?)(?:[ \t]+#+)?[ \t]*$|^ {0,3}(#{1,6})[ \t]*$`)
	fenceRE  = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})")
	setextH1 = regexp.MustCompile(`^ {0,3}=+[ \t]*$`)
	setextH2 = regexp.MustCompile(`^ {0,3}-+[ \t]*$`)
	noiseRE  = regexp.MustCompile(`^[` + textutil.Space + `]*\[\[_(TOC|TOSP)_\]\][` + textutil.Space + `]*$`)
	linkRE   = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	emphRE   = regexp.MustCompile("[*_`]")
)

// FenceRE is shared with the chunker.
var FenceRE = fenceRE

func cleanHeading(text string) string {
	text = linkRE.ReplaceAllString(text, "${1}")
	text = emphRE.ReplaceAllString(text, "")
	return textutil.Strip(text)
}

type heading struct {
	idx   int // 0-based line index (for setext: the text line)
	level int
	text  string
}

func findHeadings(lines []string) []heading {
	var out []heading
	fence := ""
	start := 0
	if len(lines) > 0 && textutil.Strip(lines[0]) == "---" {
		for i := 1; i < len(lines); i++ {
			if s := textutil.Strip(lines[i]); s == "---" || s == "..." {
				start = i + 1
				break
			}
		}
	}
	for i := start; i < len(lines); i++ {
		line := lines[i]
		if m := fenceRE.FindStringSubmatch(line); m != nil {
			marker := m[1]
			if fence == "" {
				fence = strings.Repeat(marker[:1], len(marker))
			} else if marker[0] == fence[0] && len(marker) >= len(fence) &&
				textutil.Strip(textutil.Strip(line)[len(marker):]) == "" {
				fence = ""
			}
			continue
		}
		if fence != "" {
			continue
		}
		if m := atxRE.FindStringSubmatch(line); m != nil {
			hashes := m[1]
			if hashes == "" {
				hashes = m[3]
			}
			out = append(out, heading{i, len(hashes), cleanHeading(m[2])})
			continue
		}
		if i > start && textutil.Strip(lines[i-1]) != "" && (setextH1.MatchString(line) || setextH2.MatchString(line)) {
			prev := lines[i-1]
			p := textutil.LStrip(prev)
			prevIsBlock := atxRE.MatchString(prev) || fenceRE.MatchString(prev) ||
				strings.HasPrefix(p, "|") || strings.HasPrefix(p, ">") || strings.HasPrefix(p, "-") ||
				strings.HasPrefix(p, "*") || strings.HasPrefix(p, "+") ||
				(len(out) > 0 && out[len(out)-1].idx == i-1)
			if !prevIsBlock {
				level := 2
				if setextH1.MatchString(line) {
					level = 1
				}
				out = append(out, heading{i - 1, level, cleanHeading(prev)})
			}
		}
	}
	return out
}

type Markdown struct{}

func (Markdown) Type() string         { return "markdown" }
func (Markdown) Extensions() []string { return []string{".md", ".markdown"} }

func (Markdown) Parse(p, text string) Document {
	lines := SplitLines(text)
	headings := findHeadings(lines)
	title, hasH1 := "", false
	for _, h := range headings {
		if h.level == 1 {
			hasH1 = true
			if title == "" {
				title = h.text
			}
		}
	}
	if title == "" { // an empty H1 text falls through to the file name
		title = TitleFromPath(p)
	}
	var rootPrefix []string
	if !hasH1 {
		rootPrefix = []string{title}
	}

	type bound struct {
		idx, bodyIdx, level int
		text                string
	}
	bounds := make([]bound, 0, len(headings))
	for _, h := range headings {
		setext := h.idx+1 < len(lines) &&
			(setextH1.MatchString(lines[h.idx+1]) || setextH2.MatchString(lines[h.idx+1])) &&
			!atxRE.MatchString(lines[h.idx])
		body := h.idx + 1
		if setext {
			body = h.idx + 2
		}
		bounds = append(bounds, bound{h.idx, body, h.level, h.text})
	}

	var sections []Section
	firstStart := len(lines)
	if len(bounds) > 0 {
		firstStart = bounds[0].idx
	}
	for _, l := range lines[:firstStart] {
		if textutil.Strip(l) != "" && !noiseRE.MatchString(l) {
			sections = append(sections, mdSection(title, 0, []string{title}, 0, firstStart, lines, 0))
			break
		}
	}

	type open struct {
		level int
		text  string
	}
	var stack []open
	for n, b := range bounds {
		end := len(lines)
		if n+1 < len(bounds) {
			end = bounds[n+1].idx
		}
		for len(stack) > 0 && stack[len(stack)-1].level >= b.level {
			stack = stack[:len(stack)-1]
		}
		htext := b.text
		if htext == "" {
			htext = title
		}
		stack = append(stack, open{b.level, htext})
		prefix := rootPrefix
		// no-H1 page whose top heading repeats the page title: don't prefix twice
		if len(rootPrefix) > 0 && textutil.Casefold(stack[0].text) == textutil.Casefold(title) {
			prefix = nil
		}
		hpath := append([]string{}, prefix...)
		for _, o := range stack {
			hpath = append(hpath, o.text)
		}
		sections = append(sections, mdSection(htext, b.level, hpath, b.idx, end, lines, b.bodyIdx))
	}
	return Document{Title: title, Type: "markdown", Sections: sections}
}

func mdSection(heading string, level int, path []string, start, end int, lines []string, bodyFrom int) Section {
	// trim trailing blank lines so line ranges are tight
	for end > start+1 && textutil.Strip(lines[end-1]) == "" {
		end--
	}
	secLines := make([]string, 0, end-start)
	for _, l := range lines[start:end] {
		if noiseRE.MatchString(l) {
			l = ""
		}
		secLines = append(secLines, l)
	}
	hasBody := false
	if bodyFrom < end {
		for _, l := range lines[bodyFrom:end] {
			if !noiseRE.MatchString(l) && textutil.Strip(l) != "" {
				hasBody = true
				break
			}
		}
	}
	return Section{Heading: heading, Level: level, Path: path, StartLine: start + 1, EndLine: end,
		Lines: secLines, HasBody: hasBody}
}
