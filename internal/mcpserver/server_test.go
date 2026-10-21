package mcpserver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/skromesch/docgraph/internal/chunk"
	"github.com/skromesch/docgraph/internal/config"
	"github.com/skromesch/docgraph/internal/service"
	"github.com/skromesch/docgraph/internal/store"
)

// Tools are registered under their documented names and return the service output.
func TestMCPTools(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "A.md"), []byte("# A\n\nalma\n"), 0o644)
	db, _ := store.Open(filepath.Join(t.TempDir(), "t.db"))
	defer db.Close()
	svc := service.New(db, config.Default(), root)
	svc.EnableAutoSync(chunk.Heuristic)
	svc.SetEmbedder(nil)

	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := Build(svc, "test").Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	if init := cs.InitializeResult(); init == nil || init.Instructions != Instructions {
		t.Fatal("instructions not sent")
	}
	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range tools.Tools {
		names = append(names, tl.Name)
	}
	if strings.Join(names, ",") != "grep,list_documents,read_document,search" {
		t.Fatalf("tools %v", names)
	}
	call := func(name string, args map[string]any) string {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		return res.Content[0].(*mcp.TextContent).Text
	}
	if out := call("search", map[string]any{"query": "alma"}); !strings.HasPrefix(out, "[1] A.md:1-3") {
		t.Fatalf("search: %q", out)
	}
	if out := call("read_document", map[string]any{"path": "A.md", "start_line": 3}); out != "A.md (lines 3-3 of 3)\n\nalma" {
		t.Fatalf("read: %q", out)
	}
	if out := call("list_documents", map[string]any{}); out != "A.md | A | 1 sections" {
		t.Fatalf("list: %q", out)
	}
	if out := call("grep", map[string]any{"pattern": "ALM"}); out != "A.md (1 match)\n  3: alma\n\n[1 match in 1 file]" {
		t.Fatalf("grep: %q", out)
	}
	if out := call("grep", map[string]any{"pattern": "ALM", "ignore_case": false}); !strings.HasPrefix(out, "No lines match") {
		t.Fatalf("grep case-sensitive: %q", out)
	}
}
