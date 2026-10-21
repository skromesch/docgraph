# Evaluation

`docgraph eval` measures retrieval quality on the project index against a labelled dataset.

## Dataset format

JSON array or JSONL. One case per line:

```json
{"id": "tenant-01",
 "query": "How does the backend decide which company's data a request works with?",
 "expected_documents": ["Docs/Multitenancy.md"],
 "expected_sections": ["Multitenancy > Resolving the current company per request"],
 "tags": ["en", "paraphrase"]}
```

- Paths are relative to the indexed root, in NFC.
- `expected_sections` is optional. A result counts at section level when its `heading_path` equals an expected section or is nested below one (`A > B > C` is inside `A > B`).
- Without sections, the case is judged at document level.
- `docgraph eval` warns about expected documents or sections that do not exist in the index.
- `tags` are free-form labels, copied into the `-o` JSON results for slicing.

## Metrics

Both are computed at two levels: **section** (primary) and **document**.

- **Recall@k** for k in {1, 3, 5, 10}: the fraction of queries with at least one relevant result in the top k.
- **MRR**: the mean of `1 / rank` of the first relevant result within the top 10. A query with no relevant result contributes 0.

## Running

```bash
docgraph eval DATASET                                    # keyword, semantic and hybrid
docgraph eval DATASET --modes hybrid --set search.rrf.k=20 --set search.bm25.prefix_len=5
docgraph eval DATASET --failures hybrid                  # misses (rank > 3) with their top-3 results
docgraph eval DATASET -o results.json                    # full per-query results
```

`--set` overrides any `search.*` setting for the run without touching the config file. Settings under `index.*` or `embedding.*` change the index itself: try them on a copy of the project.

## Results that shaped the defaults

Measured on a 332-file, mostly Hungarian Azure DevOps wiki with 52 queries (42 Hungarian, 6 English, 4 identifiers). One query is about 0.02 of any metric, so differences below about 0.04 are noise. Section level, MRR (R@1 in parentheses):

| Variant | keyword | semantic | hybrid |
|---|---|---|---|
| 700 / 1200 / 100 chunks | 0.457 (0.385) | 0.533 (0.442) | 0.570 (0.462) |
| **400 / 480 / 60 chunks (default)** | 0.450 (0.346) | 0.550 (0.442) | **0.625 (0.558)** |
| bm25 `prefix_len: 5` | 0.446 | — | 0.586 |
| bm25 `min_term_len: 3` | 0.448 | — | 0.579 |
| rrf `vector_weight: 2` | — | — | 0.566 |
| rrf `k: 20` | — | — | 0.567 |
| `min_section_tokens: 50` | 0.458 | 0.533 | 0.569 |

All variants except the default row use 700 / 1200 / 100 chunks. Document-level hybrid MRR with the default is 0.735.

**Findings**:
1. Hybrid is consistently better than either retriever alone.
2. Chunks that fit e5's 512-token window give the largest gain.
3. BM25 prefix matching, RRF k and weights, and merging small sections are all within noise.
4. Remaining misses: near-duplicate pages for the same topic, English queries hijacked by generated API reference pages, identifier queries where RRF demotes a BM25 #1, and a few genuine paraphrase misses.

## Agent benchmark

Retrieval metrics do not show whether an agent answers better. The same wiki was used for an A/B benchmark with Claude Code (`claude -p`): arm A had the DocGraph MCP tools, arm B only the built-in file tools (Read, Grep, Glob). A judge model scored answers 0–2 against reference passages.

| Question set | A score | A tokens | B score | B tokens |
|---|---|---|---|---|
| 20 text questions | 2.00 | 20.6k | 1.90 | 37.4k |
| 16 diagram questions | 1.94 | 24.2k | 1.94 | 48.8k |
| 10 cross-document dependency questions | 2.00 | 28.6k | 2.00 | 40.8k |

DocGraph answered at least as well with roughly half the tokens and fewer turns, and cited the expected documents more often on diagram questions (81% vs 56%).
