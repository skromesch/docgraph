-- DocGraph SQLite schema (version 1). See docs/SCHEMA.md.
-- The vector table (chunk_embeddings, sqlite-vec vec0) is created at runtime
-- because its dimension depends on the embedding model.

PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS metadata (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS documents (
    id           TEXT PRIMARY KEY,          -- sha256(path)[:16]
    path         TEXT NOT NULL UNIQUE,      -- POSIX, relative to indexed root
    title        TEXT NOT NULL,
    type         TEXT NOT NULL,             -- markdown|plantuml|mermaid|text
    content_hash TEXT NOT NULL,             -- sha256(raw file bytes)
    size_bytes   INTEGER NOT NULL DEFAULT 0,
    modified_at  REAL NOT NULL,             -- file mtime, unix seconds
    indexed_at   REAL NOT NULL
);

CREATE TABLE IF NOT EXISTS chunks (
    rowid           INTEGER PRIMARY KEY,    -- join key for FTS5 and vec0
    id              TEXT NOT NULL UNIQUE,   -- deterministic, see hashing.chunk_id
    document_id     TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    parent_chunk_id TEXT,
    heading         TEXT NOT NULL,
    heading_path    TEXT NOT NULL,
    heading_level   INTEGER NOT NULL,
    content         TEXT NOT NULL,
    embedding_text  TEXT NOT NULL,
    content_hash    TEXT NOT NULL,          -- sha256(content)
    embedding_key   TEXT,                   -- sha256(model \x1f embedding_text); NULL = not embedded
    start_line      INTEGER NOT NULL,
    end_line        INTEGER NOT NULL,
    chunk_index     INTEGER NOT NULL,
    part            INTEGER NOT NULL DEFAULT 0,
    token_count     INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_chunks_document ON chunks(document_id);

-- Embedding cache: vectors survive chunk deletion so unchanged text is never
-- re-embedded (e.g. when a document is reparsed after an unrelated edit).
CREATE TABLE IF NOT EXISTS embedding_cache (
    key        TEXT PRIMARY KEY,
    model      TEXT NOT NULL,
    dim        INTEGER NOT NULL,
    vector     BLOB NOT NULL,               -- little-endian float32[dim], L2-normalized
    created_at REAL NOT NULL
);

-- BM25 index. External-content FTS5 table kept in sync by triggers.
-- remove_diacritics 2: "resume" matches "résumé".
CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(
    heading,
    heading_path,
    content,
    content = 'chunks',
    content_rowid = 'rowid',
    tokenize = 'unicode61 remove_diacritics 2'
);

CREATE TRIGGER IF NOT EXISTS chunks_ai AFTER INSERT ON chunks BEGIN
    INSERT INTO chunks_fts(rowid, heading, heading_path, content)
    VALUES (new.rowid, new.heading, new.heading_path, new.content);
END;

CREATE TRIGGER IF NOT EXISTS chunks_ad AFTER DELETE ON chunks BEGIN
    INSERT INTO chunks_fts(chunks_fts, rowid, heading, heading_path, content)
    VALUES ('delete', old.rowid, old.heading, old.heading_path, old.content);
END;

CREATE TRIGGER IF NOT EXISTS chunks_au AFTER UPDATE OF heading, heading_path, content ON chunks BEGIN
    INSERT INTO chunks_fts(chunks_fts, rowid, heading, heading_path, content)
    VALUES ('delete', old.rowid, old.heading, old.heading_path, old.content);
    INSERT INTO chunks_fts(rowid, heading, heading_path, content)
    VALUES (new.rowid, new.heading, new.heading_path, new.content);
END;
