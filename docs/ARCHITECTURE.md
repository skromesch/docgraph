# Architecture

## Data flow

```
                 index                                         search
 docs/ ──scan──► hash diff ──► Parser ──► Sections ──► Chunker ──► Chunks
                  │ unchanged: skip                                  │
                  │ deleted: drop doc, chunks, FTS, vectors          ▼
                  ▼                                  SQLite: documents, chunks
            Embedder (batch) ◄── embedding_cache ◄── chunks_fts (FTS5 triggers)
                  │                                  chunk_embeddings (vec0)
                  └──────────────► vectors ─────────────────┘

 query ─┬─► BM25   (FTS5 bm25, top 30) ─┐
        └─► Vector (e5 query, top 30)  ─┴─► RRF ─► top k ─► hydrate ─► results
                                                   ├─► confidence label
                                                   └─► related diagrams block
```

## Packages

| Package | Role |
|---|---|
| `internal/parse` | `Parser` interface: Markdown, PlantUML, Mermaid, plain text → sections |
| `internal/chunk` | sections → size-bounded chunks; token counters (HF tokenizer, `chars/4` heuristic) |
| `internal/embed` | `Embedder` interface; ONNX Runtime implementation (fp32 / fp16 / int8) |
| `internal/store` | SQLite repository, embedded `schema.sql`, `VectorStore` (sqlite-vec) |
| `internal/index` | scanner (`.docgraphignore`, gitignore semantics), incremental indexer |
| `internal/search` | BM25, vector search, RRF hybrid, confidence label, diagram block |
| `internal/service` | long-lived query service behind the MCP tools (search, read, list, grep, auto-sync) |
| `internal/mcpserver` | MCP server on stdio (official go-sdk) |
| `internal/eval` | dataset loader and metrics (Recall@k, MRR) |
| `internal/setup` | pinned, SHA-256-verified downloads of the model and ONNX Runtime |
| `internal/menu` | interactive `configure` menu (huh), with a plain numbered fallback |
| `internal/config` | `.docgraph/config.yaml` with defaults |
| `internal/hashing`, `internal/pyutil` | IDs and hashes; Python-compatible string semantics |
| `cmd/docgraph` | CLI (cobra); only wires the packages together |

## Key decisions

| Decision | Choice | Why / trade-off |
|---|---|---|
| Storage | Single SQLite file | Portable and inspectable. FTS5 and sqlite-vec live in the same DB, so one transaction covers all of them. |
| Vector search | sqlite-vec `vec0`, brute-force cosine | Exact search. A few thousand chunks are searched in milliseconds. No separate index file to keep in sync. ANN is unnecessary at wiki scale. |
| Join key | `chunks.rowid` (integer) | Both FTS5 and vec0 need integer rowids. The public, stable key is the text `chunks.id`. |
| Chunk boundaries | Heading sections first, size second | Retrieval returns citeable sections, and `heading_path` gives context to BM25 and the embedder. |
| Chunk size | 400 / 480 / 60 tokens | e5-base truncates at 512 tokens. In evaluation this beat 700 / 1200 / 100: hybrid MRR rose from 0.570 to 0.625. |
| Embedding text | `Document: <title> (<path>)\nSection: <heading_path>\n\n<content>` | Short sections are otherwise context-free. The original content is stored separately for display and BM25. |
| Embedding model | `intfloat/multilingual-e5-base`, ONNX fp16 | Good Hungarian and English quality, 768 dimensions. fp16 is half the size of fp32 with the same retrieval quality (cosine 1.0000 vs PyTorch). Uses `query:` / `passage:` prefixes. |
| Embedding cache | `embedding_cache` keyed by `sha256(model ␟ prefix+text)` | When a page is edited, only the changed sections are re-embedded. The cache survives chunk deletion and is pruned after each run. |
| Fusion | RRF on ranks, k = 60 | BM25 and cosine scores are not comparable. RRF needs no calibration. |
| Paths | Relative POSIX, Unicode NFC | macOS reports NFD file names. NFC keeps IDs identical across platforms. |
| Model guard | `metadata.embedding_model` / `embedding_dim` | Prevents mixing vectors from different models. A rebuild is required to switch. |
| Sync, not watch | MCP server syncs before a tool call, at most every 5 s | A size + mtime check per file, then the content hash. No file watcher to keep alive. |

## Incremental indexing

1. Scan supported files and compute `sha256(raw bytes)` for each.
2. If the hash is unchanged, skip the file. A `touch` alone changes nothing.
3. For a new or modified file: delete its old chunks (vec rows explicitly, FTS rows via trigger), parse, chunk, and insert.
4. For a file no longer on disk, or newly ignored: delete the document. Chunks cascade, and FTS and vec rows are removed.
5. Embed every chunk with `embedding_key IS NULL`, taking vectors from the cache when available.
6. Prune cache entries no chunk references.

A file that fails to parse is reported and skipped, and the run continues. A rename counts as a delete plus a new file.

## Known limitations

- No stemming for Hungarian in BM25. `prefix_len` is a crude, opt-in substitute; the `grep` tool covers word parts on demand.
- Generated API reference pages (large, English, enum-heavy) rank highly for many English queries. Exclude them with `.docgraphignore`.
- No reranker. A cross-encoder (`BAAI/bge-reranker-v2-m3`) raised section MRR from 0.625 to 0.740 in experiments, but an agent benchmark showed no gain in answer quality, at about 40× the query latency.
- Single indexed root per project (`metadata.source_root`).
- CPU embedding only. CoreML on Apple Silicon was measured: no speed-up, and 18–21 GB of memory with dynamic input shapes.
