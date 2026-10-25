package index

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/skromesch/docgraph/internal/config"
)

func TestListUnindexed(t *testing.T) {
	root := t.TempDir()
	for f, body := range map[string]string{
		"a.md": "# A\n", "b.pdf": "%PDF", "drafts/x.md": "# X\n", "node_modules/y.md": "# Y\n",
		"c.docx": "zip", "d.md": "# D\n", IgnoreFile: "drafts/\n",
	} {
		os.MkdirAll(filepath.Dir(filepath.Join(root, f)), 0o755)
		os.WriteFile(filepath.Join(root, f), []byte(body), 0o644)
	}
	got, err := ListUnindexed(root, config.Default(), false, map[string]bool{"a.md": true})
	if err != nil {
		t.Fatal(err)
	}
	want := []Unindexed{
		{IgnoreFile, "extension"},
		{"b.pdf", "extension"},
		{"c.docx", "pandoc missing"},
		{"d.md", "not indexed (failed or pending sync)"},
		{"drafts/", "ignored: drafts/"},
		{"node_modules/", "excluded dir"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("\n got  %v\n want %v", got, want)
	}
}
