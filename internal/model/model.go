// Package model holds the domain types; fields map 1:1 onto SQLite columns.
package model

type Document struct {
	ID          string // sha256(path)[:16]
	Path        string // POSIX, relative to the indexed root, NFC
	Title       string
	Type        string // markdown | plantuml | mermaid | text
	ContentHash string // sha256(raw bytes)
	SizeBytes   int64
	ModifiedAt  float64 // file mtime, unix seconds
	IndexedAt   float64
}

type Chunk struct {
	ID            string
	DocumentID    string
	ParentChunkID *string
	Heading       string
	HeadingPath   string // "A > B > C"
	HeadingLevel  int    // 0 = document root / non-markdown
	Content       string // original text
	EmbeddingText string // text fed to the embedder (without the model prefix)
	ContentHash   string // sha256(content)
	StartLine     int    // 1-based, inclusive
	EndLine       int    // 1-based, inclusive
	ChunkIndex    int
	Part          int // >0 when a large section was split
	TokenCount    int
}

type SearchResult struct {
	ChunkID      string
	DocumentPath string
	HeadingPath  string
	Content      string
	StartLine    int
	EndLine      int
	Score        float64
	Rank         int
	// per-retriever 1-based ranks filled by hybrid search; 0 = not retrieved
	Ranks map[string]int
}
