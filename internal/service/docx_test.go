package service

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/skromesch/docgraph/internal/chunk"
	"github.com/skromesch/docgraph/internal/config"
	"github.com/skromesch/docgraph/internal/convert"
	"github.com/skromesch/docgraph/internal/index"
	"github.com/skromesch/docgraph/internal/store"
	"github.com/skromesch/docgraph/internal/testutil"
)

const specMD = "# Spec\n\nBevezető szöveg.\n\n## Mezők\n\n| Név | Típus |\n|---|---|\n| ügyfélAzonosító | int |\n"

// docxService indexes a docs folder holding name (a Word document) and serves it.
func docxService(t *testing.T, name string, cfg config.Config) (*Service, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "docs")
	testutil.Docx(t, root, name, specMD)
	db, err := store.Open(filepath.Join(t.TempDir(), ".docgraph", "docgraph.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if s, err := index.New(db, cfg, nil, chunk.Heuristic).Index(root, nil, false); err != nil || s.New != 1 {
		t.Fatalf("%+v %v", s, err)
	}
	svc := New(db, cfg, root)
	svc.SetEmbedder(nil)
	return svc, root
}

func TestReadDocxLinesMatchSearch(t *testing.T) {
	svc, _ := docxService(t, "spec.docx", config.Default())
	out := svc.Search("ügyfélAzonosító", "keyword", 3)
	m := regexp.MustCompile(`spec\.docx:(\d+)-(\d+)`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no spec.docx hit:\n%s", out)
	}
	var start, end int
	for i, v := range []*int{&start, &end} {
		for _, c := range m[i+1] {
			*v = *v*10 + int(c-'0')
		}
	}
	read := svc.ReadDocument("spec.docx", start, end)
	if !strings.Contains(read, "ügyfélAzonosító") || !strings.Contains(read, "Mezők") {
		t.Fatalf("lines %d-%d:\n%s", start, end, read)
	}
}

func TestGrepDocx(t *testing.T) {
	svc, _ := docxService(t, "spec.docx", config.Default())
	if out := svc.Grep("ügyfélAzonosító", false, "*", true, 50, false); !regexp.MustCompile(`spec\.docx[\s\S]*\d+`).MatchString(out) {
		t.Fatalf("grep:\n%s", out)
	}
}

func TestReadDocxRegeneratesMissing(t *testing.T) {
	svc, _ := docxService(t, "spec.docx", config.Default())
	os.RemoveAll(convert.Dir(svc.DB.Path))
	if out := svc.ReadDocument("spec.docx", 1, 0); !strings.Contains(out, "Mezők") {
		t.Fatalf("read:\n%s", out)
	}
}

func TestReadDocxUnicodeName(t *testing.T) {
	svc, _ := docxService(t, "Ügyfél spec.docx", config.Default())
	if out := svc.ReadDocument("Ügyfél spec.docx", 1, 0); !strings.Contains(out, "Mezők") {
		t.Fatalf("read:\n%s", out)
	}
}

func TestAutoSyncWarnsOnce(t *testing.T) {
	svc, root := docxService(t, "spec.docx", config.Default())
	testutil.Docx(t, root, "new.docx", specMD)
	svc.Cfg.Convert.PandocPath = filepath.Join(t.TempDir(), "no-pandoc")
	var buf bytes.Buffer
	svc.Warn = &buf
	svc.mu.Lock()
	svc.syncLocked()
	svc.syncLocked()
	svc.mu.Unlock()
	if n := strings.Count(buf.String(), "warning: 1 .docx file(s) skipped"); n != 1 {
		t.Fatalf("%d warnings:\n%s", n, buf.String())
	}
}

func TestReadDocxMissingConvertedWithoutPandoc(t *testing.T) {
	svc, _ := docxService(t, "spec.docx", config.Default())
	os.RemoveAll(convert.Dir(svc.DB.Path))
	svc.Cfg.Convert.PandocPath = filepath.Join(t.TempDir(), "no-pandoc")
	out := svc.ReadDocument("spec.docx", 1, 0)
	if !strings.Contains(out, "converted text of spec.docx is missing and pandoc was not found") || strings.Contains(out, "skipped") {
		t.Fatalf("read:\n%s", out)
	}
}
