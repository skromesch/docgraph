// Package hashing implements DocGraph's deterministic hashes and IDs.
//
// All IDs are the first 16 hex chars of a SHA-256 digest over UTF-8 input.
// Fields are joined with the unit separator (0x1F). See docs/SCHEMA.md.
package hashing

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

const (
	Sep   = "\x1f"
	IDLen = 16
)

func SHA256Bytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func SHA256Text(s string) string { return SHA256Bytes([]byte(s)) }

func DocumentID(path string) string { return SHA256Text(path)[:IDLen] }

// ChunkID: occurrence disambiguates duplicate heading paths within one document.
func ChunkID(documentPath, headingPath string, part, occurrence int) string {
	return SHA256Text(strings.Join([]string{
		documentPath, headingPath, strconv.Itoa(occurrence), strconv.Itoa(part),
	}, Sep))[:IDLen]
}

// EmbeddingKey: identical text under the same model is never re-embedded.
// text is passage_prefix + embedding_text.
func EmbeddingKey(model, text string) string { return SHA256Text(model + Sep + text) }
