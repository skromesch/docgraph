// Package mcpserver exposes the service over MCP (stdio): search,
// read_document, list_documents and grep, with instructions for the agent.
package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/skromesch/docgraph/internal/service"
)

const Instructions = "Search the project's documentation (an Azure DevOps wiki, mostly Hungarian). " +
	"Start with `search`: it returns whole sections with path, line range and heading path, " +
	"which is often enough to answer. Each first hit of a file also lists the file's other " +
	"sections with line ranges: if the answer may span them (procedures, step lists, setup " +
	"guides), read those lines or the whole file with `read_document`. Use `list_documents` " +
	"to browse. If the hits look off-topic, retry with different wording or in the other language " +
	"(English <-> Hungarian): some pages are written in English, others in Hungarian. " +
	"Hybrid results start with a confidence label; on 'low', rephrase or grep before answering. " +
	"A 'Related diagrams' block lists matching PlantUML/Mermaid sources (flows, state machines, " +
	"data models): read them when the question is about a process, states or structure. " +
	"Exact identifiers (table names, endpoints, error codes) work best with mode='keyword'. " +
	"`grep` finds lines containing a text fragment: use it for word parts and inflected forms " +
	"(e.g. 'synchroniz' also matches 'synchronization'), codes and paths, or to find every mention " +
	"of a term; then read around the hit with `read_document`. Cite answers as path:start-end."

type SearchIn struct {
	Query string `json:"query" jsonschema:"Hungarian or English; a natural question or an exact identifier"`
	Mode  string `json:"mode,omitempty" jsonschema:"default | hybrid | keyword | semantic (default: hybrid)"`
	TopK  int    `json:"top_k,omitempty" jsonschema:"number of sections to return (default 8, max 20)"`
}

type ReadIn struct {
	Path      string `json:"path" jsonschema:"document path as shown in search results"`
	StartLine int    `json:"start_line,omitempty" jsonschema:"first line, 1-based (default 1)"`
	EndLine   int    `json:"end_line,omitempty" jsonschema:"last line, inclusive (default: end of file)"`
}

type ListIn struct {
	PathGlob string `json:"path_glob,omitempty" jsonschema:"glob filter, e.g. 'Docs/*' or '*Transaction*' (default '*')"`
}

type GrepIn struct {
	Pattern    string `json:"pattern" jsonschema:"text fragment to find (a regular expression with regex=true)"`
	Regex      bool   `json:"regex,omitempty" jsonschema:"treat pattern as a regular expression (default false)"`
	PathGlob   string `json:"path_glob,omitempty" jsonschema:"limit to a folder or file, e.g. 'Docs' (default '*')"`
	IgnoreCase *bool  `json:"ignore_case,omitempty" jsonschema:"case-insensitive (default true)"`
	MaxResults int    `json:"max_results,omitempty" jsonschema:"matching lines to return (default 50, max 200)"`
	FilesOnly  bool   `json:"files_only,omitempty" jsonschema:"list every matching file with its match count instead of the lines (default false)"`
}

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func Build(svc *service.Service, version string) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "docgraph", Version: version},
		&mcp.ServerOptions{Instructions: Instructions})

	mcp.AddTool(server, &mcp.Tool{Name: "search", Description: "Search documentation sections. Queries may be " +
		"Hungarian or English, natural questions or exact identifiers. Returns the top sections, best first, " +
		"each with path:lines, heading path and full section text. If nothing relevant comes back, rephrase " +
		"or translate the query (pages are mixed Hungarian/English) before concluding it is undocumented."},
		func(ctx context.Context, req *mcp.CallToolRequest, in SearchIn) (*mcp.CallToolResult, any, error) {
			mode := in.Mode
			if mode == "default" {
				mode = ""
			}
			k := in.TopK
			if k == 0 {
				k = 8
			}
			return text(svc.Search(in.Query, mode, k)), nil, nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "read_document", Description: "Read an indexed document (or a line range " +
		"of it) by its path as shown in search results. Returns at most 400 lines per call."},
		func(ctx context.Context, req *mcp.CallToolRequest, in ReadIn) (*mcp.CallToolResult, any, error) {
			return text(svc.ReadDocument(in.Path, in.StartLine, in.EndLine)), nil, nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "list_documents", Description: "List indexed documents as " +
		"`path | title | sections`. Filter with a glob, e.g. 'Docs/*' or '*Transaction*'."},
		func(ctx context.Context, req *mcp.CallToolRequest, in ListIn) (*mcp.CallToolResult, any, error) {
			return text(svc.ListDocuments(in.PathGlob)), nil, nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "grep", Description: "Find lines in the indexed documents that contain " +
		"`pattern` (a literal substring, or a regular expression with regex=true). Matches inside words, so a " +
		"word stem finds its inflected forms. Returns `path (N matches)` groups with `line: text`; limit to a " +
		"folder or file with path_glob. At most 200 lines. With files_only=true it lists every matching file with " +
		"its match count instead of the lines: use it to find all documents that mention a table, component or " +
		"code (dependency and impact questions)."},
		func(ctx context.Context, req *mcp.CallToolRequest, in GrepIn) (*mcp.CallToolResult, any, error) {
			ic := in.IgnoreCase == nil || *in.IgnoreCase
			n := in.MaxResults
			if n == 0 {
				n = 50
			}
			return text(svc.Grep(in.Pattern, in.Regex, in.PathGlob, ic, n, in.FilesOnly)), nil, nil
		})
	return server
}

// RunStdio serves until the client closes stdin.
func RunStdio(ctx context.Context, server *mcp.Server) error {
	return server.Run(ctx, &mcp.StdioTransport{})
}
