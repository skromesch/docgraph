# DocGraph

Local search over a documentation folder (Markdown, PlantUML, Mermaid, text) for AI coding agents. DocGraph indexes the files into one SQLite database and serves hybrid search (BM25 + multilingual embeddings, fused with RRF) through an MCP server, so an agent such as Claude Code finds the right section instead of reading files one by one.

- **Hybrid search**, tuned for mixed Hungarian/English wikis (`multilingual-e5-base`), with a confidence label and a separate block for related diagrams.
- **MCP tools**: `search`, `read_document`, `list_documents`, `grep`.
- **Incremental**: only changed files are re-parsed and re-embedded, and the MCP server syncs before tool calls.
- **Local**: one binary, the model runs on the CPU via ONNX Runtime, nothing leaves the machine.

In an agent benchmark on a 332-file wiki, Claude Code with DocGraph answered as well as or better than with plain file tools, using about half the tokens ([details](docs/EVALUATION.md#agent-benchmark)).

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/skromesch/docgraph/main/install.sh | sh
```

`install.sh`:

1. downloads the archive for your OS and CPU and checks it against `checksums.txt`;
2. installs the binary to `~/.local/bin`, or to `/usr/local/bin` when run as root;
3. runs `docgraph model pull` (about 600 MB, once).

Options: `--version v0.1.0`, `--dir DIR`, `--no-model` (pass them as `sh -s -- --version v0.1.0` when piping).

Releases exist for macOS arm64 and Linux amd64/arm64 (glibc 2.36+: Debian 12, Ubuntu 22.04, RHEL 9 and newer). Intel macOS is not supported, because ONNX Runtime publishes no prebuilt library for it.

**Windows**: there is no native build yet. Use [WSL](https://learn.microsoft.com/windows/wsl/install) and install the Linux build inside it with the `install.sh` command above. A Windows-side MCP client can start the server through WSL. Use the full path, because `wsl` does not read your shell profile, so `~/.local/bin` may not be on its PATH:

```json
{"mcpServers": {"docgraph": {"type": "stdio", "command": "wsl", "args": ["/home/me/.local/bin/docgraph", "mcp", "--project", "/home/me/project"]}}}
```

Keep the project inside the WSL file system (e.g. `/home/me/project`): files under `/mnt/c/...` work, but scanning them is much slower.

## First steps

```bash
cd my-project
docgraph            # interactive menu: init, choose the docs folder, review ignore rules,
                    # index, sync, model download, try a search, register with Claude Code
```

The menu runs as plain numbered prompts with `docgraph configure --plain`, and automatically when the input is not a terminal.

Without the menu:

```bash
docgraph init                      # .docgraph/config.yaml + docgraph.db in the current directory
docgraph index docs/               # the first run embeds every chunk (minutes on CPU)
docgraph sync                      # later: one incremental run
docgraph search "how is an inventory closed"
docgraph search -m keyword "X-Company-Id"
docgraph stats
```

## Use with Claude Code

```bash
claude mcp add docgraph -- docgraph mcp --project /abs/path/to/project
```

Add `--scope user` to register it for every project. The menu prints the same command, and a JSON snippet for other MCP clients:

```json
{"mcpServers": {"docgraph": {"type": "stdio", "command": "docgraph", "args": ["mcp", "--project", "/abs/path/to/project"]}}}
```

`docgraph mcp` opens the database at once and loads the model in the background (keyword search works meanwhile). Before a tool call it syncs at most every 5 s: it compares size and mtime per file and re-indexes only changed, new or deleted files. Flags: `--no-sync`, `--keyword-only`.

| Tool | Purpose |
|---|---|
| `search(query, mode="hybrid", top_k=8)` | top sections as `path:start-end`, heading path and section text; in hybrid mode a confidence label (`high` / `medium` / `low`) and up to 3 related PlantUML/Mermaid diagrams |
| `read_document(path, start_line=1, end_line=None)` | an indexed file or a line range of it (max 400 lines per call) |
| `list_documents(path_glob="*")` | `path \| title \| sections` |
| `grep(pattern, regex=False, path_glob="*", ignore_case=True, max_results=50, files_only=False)` | lines containing a substring or RE2 regex, grouped by file; `files_only` lists every matching file with its match count |

See [docs/RETRIEVAL.md](docs/RETRIEVAL.md) for how the confidence label and the diagram block are computed.

## Excluding files

DocGraph reads `.docgraphignore` in the root of the indexed folder. It uses `.gitignore` syntax:

```gitignore
# generated API reference
**/GeneratedDocs/
Archive/
*.txt
# re-include:
!Important.txt
# only at the root:
/Draft.md
```

AI agent instruction files are excluded by default, at any depth: `CLAUDE.md`, `AGENTS.md`, `GEMINI.md`, `.claude/`, `.cursor/`, `.github/copilot-instructions.md` and similar (the full list is `index.ignore` in `.docgraph/config.yaml`). A line like `!CLAUDE.md` in `.docgraphignore` re-includes a file. The config patterns come first, so the file can re-include what the config excludes; a file under an excluded directory cannot be re-included, the same as in git.

The menu's ignore editor previews which files a rule would exclude before saving. Documents that become ignored are removed from the index on the next sync.

## Supported files

| Extension | Parser | Chunking |
|---|---|---|
| `.md`, `.markdown` | Markdown (ATX/Setext headings, fences, front matter, Azure DevOps `[[_TOC_]]`) | by heading hierarchy, then size |
| `.puml`, `.plantuml` | PlantUML | one section per `@start…@end` block |
| `.mmd`, `.mermaid` | Mermaid | whole file |
| `.txt` | plain text | whole file, split by size |

File names follow the Azure DevOps wiki convention: `Bulk-asset%2Dassignment.md` gets the title `Bulk asset-assignment`. See [docs/CHUNKING.md](docs/CHUNKING.md).

## Configuration

`docgraph init` writes `.docgraph/config.yaml`. Missing keys keep their defaults.

```yaml
index:
  target_tokens: 400        # pack split sections up to this size
  max_tokens: 480           # sections above this are split (e5 input limit is 512)
  overlap_tokens: 60
  min_section_tokens: 0     # >0: merge tiny subsections into their parent
  extensions: [.md, .markdown, .puml, .plantuml, .mmd, .mermaid, .txt]
  ignore: [CLAUDE.md, AGENTS.md, .claude/, …]   # gitignore patterns (+ <root>/.docgraphignore)
  exclude_dirs: [.git, .docgraph, .venv, node_modules, __pycache__]
embedding:
  model: intfloat/multilingual-e5-base
  backend: onnx-fp16        # onnx (fp32) | onnx-fp16 | onnx-int8
  batch_size: 32
  query_prefix: null        # null = derived from the model (e5: "query: ")
  passage_prefix: null      # null = derived from the model (e5: "passage: ")
search:
  mode: hybrid              # keyword | semantic | hybrid
  top_k: 10
  bm25_candidates: 30
  vector_candidates: 30
  bm25:
    heading_weight: 5
    heading_path_weight: 3
    content_weight: 1
    prefix_len: 0           # >0: crude stemming, query terms truncated + prefix match
    min_term_len: 1
  rrf:
    k: 60
    bm25_weight: 1
    vector_weight: 1
```

Switching `embedding.model`, or to and from int8, requires `docgraph index --rebuild`; vectors from different models are never mixed. fp32 and fp16 vectors are interchangeable.

## Model and runtime

`docgraph model pull` downloads artifacts pinned by URL and revision and verified by SHA-256; `docgraph model status` shows where they are.

- **Model**: the fp16 ONNX export of `multilingual-e5-base` (`Xenova/multilingual-e5-base`, 555 MB) and its `tokenizer.json`, into `~/.cache/docgraph/onnx/`. Against the PyTorch sentence-transformers vectors the minimum cosine is 1.00000.
- **ONNX Runtime** 1.31.0 from Microsoft's GitHub release, into `~/.cache/docgraph/lib/`.

Override the locations with `DOCGRAPH_MODEL_DIR`, `DOCGRAPH_LIB_DIR` or `ONNXRUNTIME_LIB`. Without the model, `search -m keyword`, `index --no-embed` and `mcp --keyword-only` still work.

## Evaluation

```bash
docgraph eval dataset.jsonl --modes hybrid --failures hybrid
```

Recall@k and MRR at section and document level against a labelled JSONL dataset; `--set search.rrf.k=20` tries a setting without editing the config. See [docs/EVALUATION.md](docs/EVALUATION.md).

## Development

Requirements: [mise](https://mise.jdx.dev) (installs Go), a C compiler (cgo: SQLite, sqlite-vec, tokenizers), and Rust/cargo to compile the Hugging Face tokenizers static library once.

```bash
mise run build        # -> bin/docgraph (compiles lib/libtokenizers.a on the first run)
mise run test         # model-dependent tests skip until `docgraph model pull`
mise run golden       # rewrite testdata/golden after an intended change; review the diff
mise run dist v0.1.0  # release archives + checksums.txt into dist/
```

mise also sets the cgo flags and the `sqlite_fts5` build tag, so plain `go build` and `go test` work inside the project.

Tests run on a small fixture wiki (`testdata/fixtures/wiki`). Golden files in `testdata/golden` pin chunking, index rows, BM25 and hybrid rankings, tool outputs and embedding vectors. They guard against regressions.

`mise run dist` builds darwin/arm64 natively, and linux/amd64 and linux/arm64 in `golang:<ver>-bookworm` containers (Docker), using daulet/tokenizers' prebuilt `libtokenizers.a`. Upload the archives and `checksums.txt` to a GitHub release; `install.sh` picks them up.

## Documentation

- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md): data flow, packages, key decisions
- [docs/CHUNKING.md](docs/CHUNKING.md): parsing and chunking rules
- [docs/RETRIEVAL.md](docs/RETRIEVAL.md): BM25, vector search, RRF, confidence label, diagrams, grep
- [docs/SCHEMA.md](docs/SCHEMA.md): SQLite schema and ID formulas
- [docs/EVALUATION.md](docs/EVALUATION.md): dataset format, metrics, measured results
