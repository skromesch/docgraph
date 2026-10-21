# Parsing and chunking

Two stages:

1. A **parser** turns text into a flat list of *sections*. Each section is a contiguous line range with a heading hierarchy.
2. The **chunker** turns sections into size-bounded *chunks*.

Parsers know nothing about token limits.

## Common rules

- Lines are split on `\r\n`, `\r` or `\n`. Line numbers are 1-based and inclusive.
- Files are decoded as UTF-8 (a BOM is stripped). Invalid bytes are replaced with U+FFFD.
- The document path is relative POSIX in Unicode NFC.
- **Title from path** (Azure DevOps convention): take the file stem in NFC, replace `-` with a space, then percent-decode. Example: `Bulk-asset%2Dassignment.md` → `Bulk asset-assignment`.

## Markdown parser

**Heading recognition** (a CommonMark subset):
- **ATX**: up to 3 spaces of indent, 1–6 `#`, then whitespace and text. A trailing run of `#` is removed. A bare `#` is an empty heading.
- **Setext**: a non-blank line followed by `===` (H1) or `---` (H2). The text line must not be a heading, fence, table (`|`), quote (`>`) or list item (`-`, `*`, `+`).
- **Fenced code**: a line starting with 3+ backticks or tildes opens a block. A later fence of the same character and at least the same length closes it. Nothing inside is a heading.
- **YAML front matter**: if line 1 is `---`, everything up to the next `---` or `...` is skipped for heading detection.
- **Heading text cleanup**: `[label](url)` becomes `label`, and `*`, `_` and `` ` `` are removed.
- **Azure DevOps markers**: `[[_TOC_]]` and `[[_TOSP_]]` are blanked in section bodies.

**Title**: the first H1 heading, otherwise the title from path.

**Hierarchy**:
- Keep a stack of `(level, heading)`. For each heading, pop entries with level ≥ the current level, then push the current one.
- `path` = the stack's headings.
- If the document has **no H1**, every path is prefixed with the title. The exception: when the top stack entry already equals the title (case-insensitively), no prefix is added, to avoid `Inventory > Inventory`.

**Sections**:
- Lines before the first heading form a level-0 section named after the title. It is created only if it contains non-blank, non-marker text.
- A heading section spans from its heading line up to the line before the next heading of any level. A section contains only its **own** text, not its children's.
- Trailing blank lines are trimmed from the range.
- `has_body` is false when nothing but the heading line (and blanks or markers) is present.

## PlantUML / Mermaid / text

- **PlantUML**:
  - every `@startXXX [name]` … `@endXXX` block is one level-0 section;
  - section heading: the first `title` directive, else the `@start` name, else the title from path;
  - section path: `[file title]` or `[file title, heading]`;
  - if markers are missing or unterminated, the whole file is one section.
- **Mermaid**: the whole file is one section, titled from `title:` (front matter or directive) or the path.
- **Text**: the whole file is one section.

## Chunker

Options: `target_tokens` (400), `max_tokens` (480), `overlap_tokens` (60), `min_section_tokens` (0).

For each section, in document order:

1. **Skip** sections with `has_body = false`. The heading still appears in its children's `heading_path`.
2. **Merge** (only if `min_section_tokens > 0`): a section with fewer tokens than the threshold, whose previous kept section is its ancestor, is appended to that section, including the blank lines between them.
3. `heading_path = " > ".join(path)`. `occurrence` = how many times this `heading_path` was already seen in this document.
4. **Parent** = the chunk ID of the first part of the nearest ancestor path that produced a chunk, else null.
5. If `count(section text) <= max_tokens`, the section becomes **one chunk** with `part = 0`.
6. Otherwise **split**:
   1. **Blocks.** A fenced code block (fence to closing fence) is one block. Consecutive lines starting with `|` (a table) form one block. Everything else is paragraphs separated by blank lines.
   2. **Atomize.** A block over `max_tokens` is broken into single lines. A line over `max_tokens` is broken into word runs: words are added while the run stays within `target_tokens`.
   3. **Pack** greedily.
      - Append units to the current piece.
      - When adding a unit would exceed `target_tokens` and the piece already contains new (non-overlap) lines, close the piece.
      - Start the next piece with the **overlap tail**: the longest suffix of whole lines from the previous piece with at most `overlap_tokens` tokens, and never the whole piece. If the tail plus the next unit would exceed `max_tokens`, drop the tail.
      - A blank line is re-inserted between non-adjacent units to preserve paragraph breaks.
   4. Parts are numbered `1..n`. Leading and trailing blank lines of each part are trimmed before line numbers are taken.
7. **Chunk fields**:
   - `content` = the part's lines joined with `\n`, stripped of leading and trailing newlines;
   - `content_hash` = `sha256(content)`;
   - `token_count` = `count(content)`;
   - `id = sha256(path ␟ heading_path ␟ occurrence ␟ part)[:16]`;
   - `embedding_text` = see below.

**Embedding text**:

```
Document: <title> (<path>)
Section: <heading_path>[ (part i/n)]

<content>
```

The embedder adds its own prefix (`passage: ` for e5) at embedding time. The cache key covers the prefix as well.

## Token counting

The default is the embedding model's HF tokenizer with `add_special_tokens=False`. If the tokenizer cannot be loaded, it falls back to `ceil(len(chars) / 4)`. The e5 model has a 512-token input limit. `max_tokens = 480` leaves room for the `Document:` / `Section:` header and the prefix. Very long heading paths can still push the input over 512 tokens, in which case the model truncates the end of the chunk.

## Observed on a real wiki (332 files, mostly Hungarian)

| Setting | Chunks | Note |
|---|---|---|
| 700 / 1200 / 100 | 2354 | 313 chunks above 512 tokens, so their tails were invisible to e5 |
| 400 / 480 / 60 (default) | 3027 | best hybrid MRR in evaluation |
