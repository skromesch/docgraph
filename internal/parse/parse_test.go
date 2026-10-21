package parse

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// Expected values in testdata/parse_cases.json were produced by the original Python implementation.
type cases struct {
	Titles map[string]string `json:"titles"`
	MD     map[string]struct {
		Text     string  `json:"text"`
		Title    string  `json:"title"`
		Sections [][]any `json:"sections"`
	} `json:"md"`
}

func load(t *testing.T) cases {
	data, err := os.ReadFile("../../testdata/parse_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var c cases
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestTitleFromPath(t *testing.T) {
	for p, want := range load(t).Titles {
		if got := TitleFromPath(p); got != want {
			t.Errorf("TitleFromPath(%q) = %q, want %q", p, got, want)
		}
	}
}

func TestMarkdownEdgeCases(t *testing.T) {
	for name, c := range load(t).MD {
		d := For(name).Parse(name, c.Text)
		if d.Title != c.Title {
			t.Errorf("%s: title %q want %q", name, d.Title, c.Title)
		}
		got := make([][]any, len(d.Sections))
		for i, s := range d.Sections {
			path := make([]any, len(s.Path))
			for j, p := range s.Path {
				path[j] = p
			}
			lines := make([]any, len(s.Lines))
			for j, l := range s.Lines {
				lines[j] = l
			}
			got[i] = []any{s.Heading, float64(s.Level), path, float64(s.StartLine), float64(s.EndLine), s.HasBody, lines}
		}
		if len(got) == 0 && len(c.Sections) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, c.Sections) {
			t.Errorf("%s:\n got  %v\n want %v", name, got, c.Sections)
		}
	}
}
