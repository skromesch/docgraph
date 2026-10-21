package chunk

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"golang.org/x/text/unicode/norm"

	"github.com/skromesch/docgraph/internal/config"
	"github.com/skromesch/docgraph/internal/model"
	"github.com/skromesch/docgraph/internal/parse"
	"github.com/skromesch/docgraph/internal/pyutil"
	"github.com/skromesch/docgraph/internal/testutil"
)

type goldenChunk struct {
	ID                string  `json:"id"`
	Parent            *string `json:"parent"`
	Heading           string  `json:"heading"`
	HeadingPath       string  `json:"heading_path"`
	Level             int     `json:"level"`
	Start             int     `json:"start"`
	End               int     `json:"end"`
	Index             int     `json:"index"`
	Part              int     `json:"part"`
	Tokens            int     `json:"tokens"`
	ContentHash       string  `json:"content_hash"`
	EmbeddingTextHash string  `json:"embedding_text_hash"`
}

type goldenDoc struct {
	Opts     []int         `json:"opts"`
	Path     string        `json:"path"`
	File     string        `json:"file"`
	Title    string        `json:"title"`
	Type     string        `json:"type"`
	Sections [][]any       `json:"sections"`
	Chunks   []goldenChunk `json:"chunks"`
}

// runGolden chunks every document listed in testdata/golden/<name> and
// compares the result; with -update it rechunks the fixture wiki and rewrites
// the file.
func runGolden(t *testing.T, name string, opts Options, count Counter) {
	root := testutil.Fixtures()
	if testutil.Update() {
		writeGolden(t, name, root, opts, count)
		return
	}
	f, err := os.Open(testutil.GoldenPath(name))
	if err != nil {
		t.Fatalf("%v (run go test -update to create it)", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	docs, chunks, bad := 0, 0, 0
	for sc.Scan() {
		var g goldenDoc
		if err := json.Unmarshal(sc.Bytes(), &g); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(root, g.File))
		if err != nil {
			raw, err = os.ReadFile(filepath.Join(root, norm.NFD.String(g.File)))
			if err != nil {
				t.Fatalf("%s: %v", g.Path, err)
			}
		}
		parsed := parse.For(g.Path).Parse(g.Path, pyutil.DecodeFile(raw))
		got := goldenChunks(Document(g.Path, parsed, opts, count))
		docs++
		chunks += len(got)
		if parsed.Title != g.Title || parsed.Type != g.Type {
			t.Errorf("%s: title/type %q/%q want %q/%q", g.Path, parsed.Title, parsed.Type, g.Title, g.Type)
			bad++
			continue
		}
		if len(got) != len(g.Chunks) {
			t.Errorf("%s: %d chunks, want %d", g.Path, len(got), len(g.Chunks))
			bad++
			continue
		}
		for i := range got {
			if !reflect.DeepEqual(got[i], g.Chunks[i]) {
				t.Errorf("%s chunk %d:\n got  %+v\n want %+v", g.Path, i, got[i], g.Chunks[i])
				bad++
				break
			}
		}
		if bad > 10 {
			t.Fatal("too many mismatches")
		}
	}
	if docs == 0 {
		t.Fatalf("%s lists no documents", name)
	}
	t.Logf("%s: %d documents, %d chunks compared", name, docs, chunks)
}

func goldenChunks(cs []model.Chunk) []goldenChunk {
	out := make([]goldenChunk, len(cs))
	for i, c := range cs {
		sum := sha256.Sum256([]byte(c.EmbeddingText))
		out[i] = goldenChunk{c.ID, c.ParentChunkID, c.Heading, c.HeadingPath, c.HeadingLevel,
			c.StartLine, c.EndLine, c.ChunkIndex, c.Part, c.TokenCount, c.ContentHash, hex.EncodeToString(sum[:])}
	}
	return out
}

func writeGolden(t *testing.T, name, root string, opts Options, count Counter) {
	exts := map[string]bool{}
	for _, e := range config.Default().Index.Extensions {
		exts[e] = true
	}
	var files []string
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && exts[filepath.Ext(p)] {
			rel, _ := filepath.Rel(root, p)
			files = append(files, filepath.ToSlash(rel))
		}
		return err
	})
	sort.Strings(files)
	var buf bytes.Buffer
	for _, file := range files {
		raw, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Fatal(err)
		}
		path := norm.NFC.String(file)
		parsed := parse.For(path).Parse(path, pyutil.DecodeFile(raw))
		sections := make([][]any, len(parsed.Sections))
		for i, s := range parsed.Sections {
			sections[i] = []any{s.Heading, s.Level, s.Path, s.StartLine, s.EndLine, s.HasBody}
		}
		line, _ := json.Marshal(goldenDoc{
			Opts: []int{opts.TargetTokens, opts.MaxTokens, opts.OverlapTokens, opts.MinSectionTokens},
			Path: path, File: file, Title: parsed.Title, Type: parsed.Type, Sections: sections,
			Chunks: goldenChunks(Document(path, parsed, opts, count)),
		})
		buf.Write(append(line, '\n'))
	}
	testutil.Golden(t, name, buf.Bytes())
}

func hfCounter(t *testing.T) Counter {
	p, err := FindTokenizer("intfloat/multilingual-e5-base")
	if err != nil {
		t.Skip(err)
	}
	c, err := NewHFCounter(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c.Count
}

// Small chunks exercise splitting, overlap and section merging.
var smallHeuristic, smallHF = Options{60, 90, 20, 30}, Options{80, 100, 25, 40}

func TestGoldenFixturesHeuristic(t *testing.T) {
	runGolden(t, "fixtures-heuristic.jsonl", DefaultOptions(), Heuristic)
}

func TestGoldenFixturesHF(t *testing.T) {
	runGolden(t, "fixtures-hf.jsonl", DefaultOptions(), hfCounter(t))
}

func TestGoldenFixturesSmallHeuristic(t *testing.T) {
	runGolden(t, "fixtures-small-heuristic.jsonl", smallHeuristic, Heuristic)
}

func TestGoldenFixturesSmallHF(t *testing.T) {
	runGolden(t, "fixtures-small-hf.jsonl", smallHF, hfCounter(t))
}
