# Retrieval

There are three modes: `keyword` (BM25), `semantic` (vector) and `hybrid` (RRF fusion of both, the default).
Every mode returns chunks. A result carries the document path, heading path, line range, content, score and per-retriever ranks.

## BM25 (SQLite FTS5)

- **Index**: `chunks_fts(heading, heading_path, content)`, tokenizer `unicode61 remove_diacritics 2`. Matching is case- and accent-insensitive, so `resume` matches `résumé`.
- **Query construction** (`search.BuildMatchQuery`):
  1. Extract `\w+` words (Unicode) from the lowercased query.
  2. Drop words shorter than `min_term_len`.
  3. If `prefix_len > 0` and the word is longer than that, emit `"<first prefix_len chars>"*`. Otherwise emit `"<word>"`.
  4. De-duplicate while keeping order, then join with ` OR `.
  
  User input is never passed through as FTS5 syntax, so `OR`, `NOT`, `*` and quotes are treated as plain words.
- **Ranking**: `ORDER BY bm25(chunks_fts, w_heading, w_heading_path, w_content)` with defaults 5 / 3 / 1. bm25() is negative and lower is better, so the reported score is `-bm25`.

## Vector search (sqlite-vec)

1. Embed the query as `query: <text>` for e5 and L2-normalize it.
2. Run `SELECT rowid, distance FROM chunk_embeddings WHERE embedding MATCH ? AND k = ? ORDER BY distance`. The search is exact (brute force) with `distance_metric=cosine`.
3. Report `similarity = 1 - distance`.

## Hybrid: Reciprocal Rank Fusion

```
candidates_bm25   = BM25 top bm25_candidates   (30)
candidates_vector = vector top vector_candidates (30)

RRF(d) = Σ_r  w_r / (k + rank_r(d))      rank is 1-based; absent ⇒ no contribution
k = 60, w_bm25 = w_vector = 1
```

- If an item appears more than once in one list, only its first (best) rank counts.
- **Ordering**: descending RRF score, then ascending best rank across retrievers, then the order of first appearance when scanning lists in the order bm25, vector. This is fully deterministic.
- The top `top_k` items are returned. `score` is the RRF score, and `ranks = {bm25: r, vector: r}` (0 = not retrieved).

Raw BM25 and cosine values are never combined, because their scales are unrelated.

**Worked example** (k = 60): a chunk ranked bm25 #2 and vector #1 scores `1/62 + 1/61 = 0.03252`. A chunk ranked only bm25 #1 scores `1/61 = 0.01639`.

## Observed behaviour (a 332-file Hungarian wiki, 52-query dataset)

- Hybrid beats either retriever alone on every chunking variant. See [EVALUATION.md](EVALUATION.md).
- **Exact identifier queries** (`X-Company-Id`): BM25 places the right chunk at #1, but RRF can push it down to about #5 when the vector list does not contain it. Candidate fixes: a reranker, or detecting identifier-like queries and raising `bm25_weight` for them.
- **English queries** against Hungarian prose are dominated by generated English API reference pages.

## Confidence label

RRF scores only reflect rank, so the best hit always scores well, even when it is off-topic. Hybrid search output (CLI and MCP) therefore starts with a label based on two absolute signals of the top hit: its cosine similarity to the query, and whether BM25 and the vector search both rank it in their top 5.

| label | rule |
|---|---|
| `low` | the top hit is not among the vector candidates, or the best cosine < 0.83 |
| `high` | the best cosine ≥ 0.865 and both retrievers rank the top hit in their top 5 |
| `medium` | otherwise |

The low label tells the agent to rephrase, try the other language, or `grep` for a word stem before concluding that something is undocumented. Calibrated for `multilingual-e5-base` on 60 questions (8 unanswerable): all 19 `high` labels were right, and 7 of 8 unanswerable questions were labelled `low`. The thresholds live in `internal/search/signals.go`; re-calibrate them for another model.

## Related diagrams block

Text chunks crowd PlantUML/Mermaid chunks out of the fused list. Diagrams therefore get their own RRF over BM25 restricted to diagram documents and the vector pool (200 deep) filtered to diagram chunks. A diagram file is listed when its best chunk is in both lists with a cosine ≥ 0.80, at most 3 files, skipping files already in the results:

```
Related diagrams (PlantUML/Mermaid source, read with read_document):
[D1] Docs/sync.mmd:1-5 | Sync flow
```

On 14 diagram questions this raised the share where the right diagram is listed from 9 to 11.

## grep

The `grep` MCP tool complements search: it finds word parts and inflected forms (`synchroniz` → `synchronization`), codes and paths that whole-word BM25 misses, and answers "which documents mention X" with `files_only=true`.

- It reads the indexed files only, so `.docgraphignore` applies.
- Literal search is case-insensitive by default; patterns and files are NFC-normalized.
- Regexes use RE2. `\w \W \d \D \s \S` are rewritten to their Unicode forms, so Python-style patterns written by an agent work as expected. Lookarounds and backreferences are not supported.
- Output is grouped by file as `line: text` (max 200 lines, lines cut at 200 characters); `files_only` lists up to 300 files with match counts.

A trigram FTS index was tested as a third RRF ranker for substring matches: section MRR dropped from 0.622 to 0.613 and document MRR from 0.745 to 0.722, while R@5 rose from 0.712 to 0.769. It was not adopted; `grep` covers that need on demand.

## Not adopted

| Idea | Result |
|---|---|
| Cross-encoder reranking (`BAAI/bge-reranker-v2-m3` over the RRF top 30) | section MRR 0.625 → 0.740 and R@1 0.558 → 0.673, but 1160 ms instead of 30 ms per query, and an agent benchmark showed no gain in answer quality. Exact identifiers lost ground (MRR 0.83 → 0.66). |
| Returning every chunk of a found document | The agent reads more with `read_document` when it needs to; results list the other sections of the file instead. |
| A separate graph/relations tool | On cross-document dependency questions, search + `grep(files_only)` scored the same as plain file tools, with fewer tokens. |
