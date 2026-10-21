package parse

import (
	"regexp"
	"strings"

	"github.com/skromesch/docgraph/internal/pyutil"
)

var (
	s         = "[" + pyutil.Space + "]"
	pumlStart = regexp.MustCompile(`(?i)^` + s + `*@start([` + pyutil.Word + `]+)(?:` + s + `+(.+?))?` + s + `*$`)
	pumlEnd   = regexp.MustCompile(`(?i)^` + s + `*@end[` + pyutil.Word + `]+` + s + `*$`)
	titleRE   = regexp.MustCompile(`(?i)^` + s + `*title` + s + `*:?` + s + `*(.+?)` + s + `*$`)
)

func titleIn(lines []string) string {
	for _, line := range lines {
		if m := titleRE.FindStringSubmatch(line); m != nil && strings.ToLower(m[1]) != "end title" {
			return strings.Trim(m[1], `"'`)
		}
	}
	return ""
}

type PlantUML struct{}

func (PlantUML) Type() string         { return "plantuml" }
func (PlantUML) Extensions() []string { return []string{".puml", ".plantuml"} }

func (PlantUML) Parse(p, text string) Document {
	lines := SplitLines(text)
	fileTitle := TitleFromPath(p)
	type block struct {
		start, end int
		name       string
		named      bool
	}
	var blocks []block
	start, name, named := -1, "", false
	for i, line := range lines {
		if m := pumlStart.FindStringSubmatchIndex(line); m != nil && start < 0 {
			start, named = i, m[4] >= 0
			if named {
				name = line[m[4]:m[5]]
			}
		} else if pumlEnd.MatchString(line) && start >= 0 {
			blocks = append(blocks, block{start, i + 1, name, named})
			start = -1
		}
	}
	if start >= 0 || len(blocks) == 0 { // unterminated or no markers: whole file
		blocks = nil
		if anyNonBlank(lines) {
			blocks = []block{{0, len(lines), "", false}}
		}
	}
	var sections []Section
	for _, b := range blocks {
		body := lines[b.start:b.end]
		heading := titleIn(body)
		if heading == "" && b.named {
			heading = strings.Trim(b.name, `"'`)
		}
		if heading == "" {
			heading = fileTitle
		}
		path := []string{fileTitle, heading}
		if heading == fileTitle {
			path = []string{fileTitle}
		}
		sections = append(sections, Section{Heading: heading, Level: 0, Path: path,
			StartLine: b.start + 1, EndLine: b.end, Lines: body, HasBody: true})
	}
	title := fileTitle
	if len(sections) == 1 {
		title = sections[0].Heading
	}
	return Document{Title: title, Type: "plantuml", Sections: sections}
}

type Mermaid struct{}

func (Mermaid) Type() string         { return "mermaid" }
func (Mermaid) Extensions() []string { return []string{".mmd", ".mermaid"} }

func (Mermaid) Parse(p, text string) Document {
	lines := SplitLines(text)
	title := titleIn(lines)
	if title == "" {
		title = TitleFromPath(p)
	}
	var sections []Section
	if anyNonBlank(lines) {
		sections = []Section{{Heading: title, Path: []string{title}, StartLine: 1, EndLine: len(lines),
			Lines: lines, HasBody: true}}
	}
	return Document{Title: title, Type: "mermaid", Sections: sections}
}

type Text struct{}

func (Text) Type() string         { return "text" }
func (Text) Extensions() []string { return []string{".txt"} }

func (Text) Parse(p, text string) Document {
	title := TitleFromPath(p)
	lines := SplitLines(text)
	var sections []Section
	if anyNonBlank(lines) {
		sections = []Section{{Heading: title, Path: []string{title}, StartLine: 1, EndLine: len(lines),
			Lines: lines, HasBody: true}}
	}
	return Document{Title: title, Type: "text", Sections: sections}
}
