package index

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/skromesch/docgraph/internal/chunk"
	"github.com/skromesch/docgraph/internal/config"
	"github.com/skromesch/docgraph/internal/convert"
	"github.com/skromesch/docgraph/internal/testutil"
)

const specMD = "# Spec\n\n## Mezők\n\n| Név | Típus |\n|---|---|\n| ügyfélAzonosító | int |\n"

// docxTree is a docs folder with spec.docx and a.md, plus an indexer on a fresh DB.
func docxTree(t *testing.T) (string, *Indexer) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "docs")
	testutil.Docx(t, root, "spec.docx", specMD)
	os.WriteFile(filepath.Join(root, "a.md"), []byte("# A\n\nszöveg\n"), 0o644)
	return root, New(newDB(t), config.Default(), nil, chunk.Heuristic)
}

func TestIndexDocx(t *testing.T) {
	root, ix := docxTree(t)
	s, err := ix.Index(root, nil, false)
	if err != nil || s.New != 2 {
		t.Fatalf("%+v %v", s, err)
	}
	if typ := count(t, ix.DB, "SELECT COUNT(*) FROM documents WHERE path='spec.docx' AND type='docx'"); typ != 1 {
		t.Fatal("spec.docx not stored as docx")
	}
	if _, err := os.Stat(convert.Path(ix.ConvertDir, "spec.docx")); err != nil {
		t.Fatal(err)
	}
	if n := count(t, ix.DB, "SELECT COUNT(*) FROM chunks c JOIN documents d ON d.id=c.document_id "+
		"WHERE d.path='spec.docx' AND c.heading_path LIKE '%Mezők%'"); n == 0 {
		t.Fatal("no chunk under Mezők")
	}
}

func TestDocxDeletedRemovesConverted(t *testing.T) {
	root, ix := docxTree(t)
	ix.Index(root, nil, false)
	os.Remove(filepath.Join(root, "spec.docx"))
	s, _ := ix.Index(root, nil, true)
	if s.Deleted != 1 {
		t.Fatalf("%+v", s)
	}
	if _, err := os.Stat(convert.Path(ix.ConvertDir, "spec.docx")); !os.IsNotExist(err) {
		t.Fatal("converted file left behind")
	}
}

func TestDocxIgnoredRemovesConverted(t *testing.T) {
	root, ix := docxTree(t)
	ix.Index(root, nil, false)
	os.WriteFile(filepath.Join(root, IgnoreFile), []byte("*.docx\n"), 0o644)
	if s, _ := ix.Index(root, nil, true); s.Deleted != 1 {
		t.Fatalf("%+v", s)
	}
	if _, err := os.Stat(convert.Path(ix.ConvertDir, "spec.docx")); !os.IsNotExist(err) {
		t.Fatal("converted file left behind")
	}
}

func TestDocxSkippedWithoutPandoc(t *testing.T) {
	root, ix := docxTree(t)
	ix.Index(root, nil, false)
	os.Remove(filepath.Join(root, "spec.docx"))
	ix.Index(root, nil, true)                   // start without the docx
	testutil.Docx(t, root, "spec.docx", specMD) // a new one arrives
	ix.Pandoc = ""                              // ... while pandoc is missing
	s, err := ix.Index(root, nil, true)
	if err != nil || s.Skipped != 1 || !reflect.DeepEqual(s.SkippedDocx, []string{"spec.docx"}) || s.Changed() {
		t.Fatalf("%+v %v", s, err)
	}
	if n := count(t, ix.DB, "SELECT COUNT(*) FROM documents WHERE path='spec.docx'"); n != 0 {
		t.Fatal("skipped docx stored")
	}
}

func TestDocxKeptWhenPandocMissing(t *testing.T) {
	root, ix := docxTree(t)
	pandoc := ix.Pandoc
	ix.Index(root, nil, false)
	before := count(t, ix.DB, "SELECT COUNT(*) FROM chunks")
	testutil.Docx(t, root, "spec.docx", specMD+"\n## Új szakasz\n\nmásik\n")
	future := time.Now().Add(time.Minute)
	os.Chtimes(filepath.Join(root, "spec.docx"), future, future)
	ix.Pandoc = ""
	s, _ := ix.Index(root, nil, true)
	if s.Skipped != 1 || s.Deleted != 0 || count(t, ix.DB, "SELECT COUNT(*) FROM chunks") != before {
		t.Fatalf("%+v", s)
	}
	ix.Pandoc = pandoc
	if s, _ := ix.Index(root, nil, true); s.Modified != 1 {
		t.Fatalf("%+v", s)
	}
}

func TestIndexDocxCorrupt(t *testing.T) {
	root, ix := docxTree(t)
	os.WriteFile(filepath.Join(root, "bad.docx"), []byte("not a zip"), 0o644)
	s, _ := ix.Index(root, nil, false)
	if s.Failed != 1 || s.New != 2 || len(s.Errors) != 1 || s.Errors[0][:9] != "bad.docx:" {
		t.Fatalf("%+v", s)
	}
}

func TestWordLockFilesIgnoredByDefault(t *testing.T) {
	root, ix := docxTree(t)
	os.WriteFile(filepath.Join(root, "~$spec.docx"), []byte("owner"), 0o644)
	if s, _ := ix.Index(root, nil, false); s.Failed != 0 || s.New != 2 {
		t.Fatalf("%+v", s)
	}
}

func TestFailedDocxNotReconvertedUntilChanged(t *testing.T) {
	root, ix := docxTree(t)
	bad := filepath.Join(root, "bad.docx")
	os.WriteFile(bad, []byte("not a zip"), 0o644)
	ix.Failures = map[string]Failure{}
	ix.Index(root, nil, true)
	ix.Pandoc = filepath.Join(t.TempDir(), "must-not-run") // a second pandoc run would error differently
	s, _ := ix.Index(root, nil, true)
	if s.Failed != 1 || len(s.Errors) != 1 || strings.Contains(s.Errors[0], "must-not-run") {
		t.Fatalf("re-ran pandoc: %+v", s)
	}
	future := time.Now().Add(time.Minute)
	os.Chtimes(bad, future, future)
	if s, _ := ix.Index(root, nil, true); !strings.Contains(s.Errors[0], "must-not-run") {
		t.Fatalf("changed file not retried: %+v", s)
	}
}
