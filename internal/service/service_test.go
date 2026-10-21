package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/skromesch/docgraph/internal/chunk"
	"github.com/skromesch/docgraph/internal/config"
	"github.com/skromesch/docgraph/internal/embed"
	"github.com/skromesch/docgraph/internal/store"
	"github.com/skromesch/docgraph/internal/testutil"
	"github.com/skromesch/docgraph/internal/testutil/fixture"
)

type toolCall struct {
	Tool   string         `json:"tool"`
	Args   map[string]any `json:"args"`
	Output string         `json:"output"`
}

// call runs one MCP tool by name with JSON-like args (MCP defaults applied).
func call(svc *Service, tool string, a map[string]any) string {
	num := func(k string, def int) int {
		if v, ok := a[k].(int); ok {
			return v
		}
		return def
	}
	str := func(k string) string { s, _ := a[k].(string); return s }
	flag := func(k string, def bool) bool {
		if v, ok := a[k].(bool); ok {
			return v
		}
		return def
	}
	switch tool {
	case "search":
		return svc.Search(str("query"), str("mode"), num("top_k", 8))
	case "read_document":
		return svc.ReadDocument(str("path"), num("start_line", 1), num("end_line", 0))
	case "list_documents":
		return svc.ListDocuments(str("path_glob"))
	case "grep":
		glob := str("path_glob")
		if glob == "" {
			glob = "*"
		}
		return svc.Grep(str("pattern"), flag("regex", false), glob, flag("ignore_case", true), num("max_results", 50), flag("files_only", false))
	}
	panic(tool)
}

func runCalls(svc *Service, calls []toolCall) []toolCall {
	for i := range calls {
		calls[i].Output = call(svc, calls[i].Tool, calls[i].Args)
	}
	return calls
}

type args = map[string]any

// Tool outputs over the fixture wiki without a model (keyword search, read,
// list, grep).
func TestToolOutputsGolden(t *testing.T) {
	db, root := fixture.Index(t, nil)
	svc := New(db, config.Default(), root)
	svc.SetEmbedder(nil)
	testutil.GoldenJSON(t, "fixtures-service.json", runCalls(svc, []toolCall{
		{Tool: "search", Args: args{"query": "closing an inventory", "mode": "keyword"}},
		{Tool: "search", Args: args{"query": "synchronization", "mode": "keyword", "top_k": 2}},
		{Tool: "search", Args: args{"query": "push transactions", "mode": "keyword"}},
		{Tool: "search", Args: args{"query": "zzzqqq", "mode": "keyword"}},
		{Tool: "search", Args: args{"query": "   ", "mode": "keyword"}},
		{Tool: "read_document", Args: args{"path": "Inventory.md"}},
		{Tool: "read_document", Args: args{"path": "Inventory.md", "start_line": 5, "end_line": 9}},
		{Tool: "read_document", Args: args{"path": "Docs/sync.mmd", "start_line": 3}},
		{Tool: "read_document", Args: args{"path": "Inventory.md", "start_line": 500}},
		{Tool: "read_document", Args: args{"path": "Processes/Café-walkthrough.md"}},
		{Tool: "read_document", Args: args{"path": "missing.md"}},
		{Tool: "read_document", Args: args{"path": "../go.mod"}},
		{Tool: "list_documents", Args: args{}},
		{Tool: "list_documents", Args: args{"path_glob": "Docs/*"}},
		{Tool: "list_documents", Args: args{"path_glob": "*.puml"}},
		{Tool: "grep", Args: args{"pattern": "inventory"}},
		{Tool: "grep", Args: args{"pattern": "Inventory", "ignore_case": false}},
		{Tool: "grep", Args: args{"pattern": "clos", "max_results": 2}},
		{Tool: "grep", Args: args{"pattern": "a", "path_glob": "Processes/*"}},
		{Tool: "grep", Args: args{"pattern": "a", "files_only": true}},
		{Tool: "grep", Args: args{"pattern": "CAFÉ"}},
		{Tool: "grep", Args: args{"pattern": `^#+\s+\w+ing`, "regex": true}},
		{Tool: "grep", Args: args{"pattern": `\w+tion\b`, "regex": true}},
		{Tool: "grep", Args: args{"pattern": "(", "regex": true}},
		{Tool: "grep", Args: args{"pattern": "zzzqqq"}},
		{Tool: "grep", Args: args{"pattern": "   "}},
	}))
}

// Hybrid search output with the real model: confidence head and the
// Related diagrams block (skipped when the model is not pulled).
func TestHybridOutputsGolden(t *testing.T) {
	e := fixture.Embedder(t)
	db, root := fixture.Index(t, e)
	svc := New(db, config.Default(), root)
	svc.SetEmbedder(e)
	testutil.GoldenJSON(t, "fixtures-service-hybrid.json", runCalls(svc, []toolCall{
		{Tool: "search", Args: args{"query": "closing an inventory"}},
		{Tool: "search", Args: args{"query": "how does mobile synchronization work", "top_k": 3}},
		{Tool: "search", Args: args{"query": "inventory states", "top_k": 2}},
		{Tool: "search", Args: args{"query": "quantum physics particle accelerator"}},
		{Tool: "search", Args: args{"query": "inventory close", "top_k": 1}},
		{Tool: "search", Args: args{"query": "mobile synchronization push", "top_k": 1}},
		{Tool: "search", Args: args{"query": "closed inventory state", "top_k": 1}},
	}))
}

func TestAutoSyncAndBackgroundLoading(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "A.md"), []byte("# A\n\nalma\n"), 0o644)
	db, _ := store.Open(filepath.Join(t.TempDir(), "t.db"))
	defer db.Close()
	svc := New(db, config.Default(), root)
	svc.syncPeriod = 0
	svc.EnableAutoSync(chunk.Heuristic)

	gate := make(chan struct{})
	svc.LoadModelInBackground(func() (embed.Embedder, error) { <-gate; return nil, nil })
	// keyword search does not wait for the model; auto-sync indexes the new root
	if out := svc.Search("alma", "keyword", 5); !strings.HasPrefix(out, "[1] A.md:1-3") {
		t.Fatalf("got %q", out)
	}
	os.WriteFile(filepath.Join(root, "B.md"), []byte("# B\n\nquarkcloud\n"), 0o644)
	if out := svc.Search("quarkcloud", "keyword", 5); !strings.HasPrefix(out, "[1] B.md:") {
		t.Fatalf("new file not synced: %q", out)
	}
	os.Remove(filepath.Join(root, "B.md"))
	if out := svc.Search("quarkcloud", "keyword", 5); out != "No results." {
		t.Fatalf("deleted file still found: %q", out)
	}
	close(gate)
	done := make(chan string)
	go func() { done <- svc.Search("alma", "", 5) }() // waits for the (nil) model, then keyword
	select {
	case out := <-done:
		if !strings.HasPrefix(out, "[1] A.md") {
			t.Fatalf("got %q", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("search did not return after loading")
	}
}

func TestReadDocumentSafety(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "A.md"), []byte("# A\n\n1\n2\n3\n"), 0o644)
	db, _ := store.Open(filepath.Join(t.TempDir(), "t.db"))
	defer db.Close()
	svc := New(db, config.Default(), root)
	svc.EnableAutoSync(chunk.Heuristic)
	svc.SetEmbedder(nil)
	if out := svc.ReadDocument("A.md", 3, 4); out != "A.md (lines 3-4 of 5)\n\n1\n2" {
		t.Fatalf("got %q", out)
	}
	for _, p := range []string{"../../etc/passwd", "/etc/passwd", "B.md"} {
		if out := svc.ReadDocument(p, 1, 0); !strings.HasPrefix(out, "Not an indexed document") {
			t.Fatalf("%s: %q", p, out)
		}
	}
}
