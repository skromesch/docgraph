# DocGraph SQLite schema (v1)

Database file: `.docgraph/docgraph.db`. Source of truth: [`internal/store/schema.sql`](../internal/store/schema.sql).
SQLite is built with FTS5, and the [sqlite-vec](https://github.com/asg017/sqlite-vec) extension is compiled into the binary.

## Identifiers

All IDs and hashes are SHA-256 over UTF-8, hex encoded. IDs are truncated to 16 hex chars.
Multi-part keys are joined with `\x1f` (unit separator).

| Value | Formula |
|---|---|
| `documents.id` | `sha256(path)[:16]`, where `path` is POSIX, relative to the indexed root, Unicode **NFC** |
| `documents.content_hash` | `sha256(raw file bytes)` |
| `chunks.id` | `sha256(document_path ␟ heading_path ␟ occurrence ␟ part)[:16]` |
| `chunks.content_hash` | `sha256(content)` |
| `chunks.embedding_key` | `sha256(model ␟ passage_prefix + embedding_text)` (full length; NULL = not yet embedded) |

`occurrence` is how many times the same `heading_path` already appeared in that document (0, 1, …).
`part` is 0 for an unsplit section and 1..n for the pieces of a split section.

## Tables

### `metadata` (key/value)
| key | meaning |
|---|---|
| `schema_version` | `1` |
| `embedding_model` | model name the vectors were built with |
| `embedding_dim` | vector dimension |
| `source_root` | absolute path of the indexed directory |
| `last_indexed_at` | unix time of the last completed run |

If the configured model or dimension differs from the stored one, the index refuses to mix vectors and asks for a rebuild.

### `documents`
`id, path (unique), title, type (markdown|plantuml|mermaid|text), content_hash, size_bytes, modified_at, indexed_at`

### `chunks`
`rowid` is an INTEGER PRIMARY KEY. It is the join key for both the FTS5 table and the vec0 table, because both require integer rowids.

`id, document_id → documents(id) ON DELETE CASCADE, parent_chunk_id, heading, heading_path ("A > B > C"), heading_level (0 = root / non-markdown), content (original text), embedding_text (what was embedded), content_hash, embedding_key, start_line, end_line (1-based, inclusive), chunk_index, part, token_count`

### `chunks_fts` (FTS5, external content = `chunks`)
Columns `heading, heading_path, content`. Tokenizer: `unicode61 remove_diacritics 2`.
Kept in sync by triggers `chunks_ai`, `chunks_ad` and `chunks_au`. `chunks_au` fires only on `UPDATE OF heading, heading_path, content`, so setting `embedding_key` does not touch FTS. Ranking uses `bm25(chunks_fts, w_heading, w_heading_path, w_content)`. The value is lower for a better match.

### `embedding_cache`
`key (= embedding_key), model, dim, vector BLOB (float32 little-endian, L2-normalized), created_at`.
This cache outlives chunks: when a document is re-chunked, any chunk whose `embedding_text` did not change reuses its vector. Unreferenced rows are pruned at the end of indexing.

### `chunk_embeddings` (sqlite-vec `vec0`, created at runtime)
```sql
CREATE VIRTUAL TABLE chunk_embeddings USING vec0(embedding float[<dim>] distance_metric=cosine);
```
`rowid = chunks.rowid`. Similarity is `1 - distance`.
