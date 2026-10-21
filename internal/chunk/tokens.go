package chunk

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/daulet/tokenizers"

	"github.com/skromesch/docgraph/internal/pyutil"
)

// Counter returns the number of tokens in a text.
type Counter func(string) int

// Heuristic is chars/4 rounded up (code points, like Python's len).
func Heuristic(text string) int { return (pyutil.Len(text) + 3) / 4 }

// HFCounter counts tokens with the model's own Hugging Face tokenizer
// (tokenizer.json), without special tokens, like sentence-transformers.
type HFCounter struct {
	mu  sync.Mutex
	tok *tokenizers.Tokenizer
}

func NewHFCounter(tokenizerJSON string) (*HFCounter, error) {
	tok, err := tokenizers.FromFile(tokenizerJSON)
	if err != nil {
		return nil, err
	}
	return &HFCounter{tok: tok}, nil
}

func (c *HFCounter) Count(text string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	ids, _ := c.tok.Encode(text, false)
	return len(ids)
}

func (c *HFCounter) Close() { c.tok.Close() }

// FindTokenizer locates tokenizer.json for a model: DOCGRAPH_MODEL_DIR /
// ~/.cache/docgraph/onnx/<org--name>/, then the Hugging Face hub cache.
func FindTokenizer(model string) (string, error) {
	slug := strings.ReplaceAll(model, "/", "--")
	home, _ := os.UserHomeDir()
	var dirs []string
	if d := os.Getenv("DOCGRAPH_MODEL_DIR"); d != "" {
		dirs = append(dirs, filepath.Join(d, slug))
	}
	dirs = append(dirs, filepath.Join(home, ".cache", "docgraph", "onnx", slug))
	hub := os.Getenv("HF_HUB_CACHE")
	if hub == "" {
		hf := os.Getenv("HF_HOME")
		if hf == "" {
			hf = filepath.Join(home, ".cache", "huggingface")
		}
		hub = filepath.Join(hf, "hub")
	}
	snaps, _ := filepath.Glob(filepath.Join(hub, "models--"+slug, "snapshots", "*"))
	dirs = append(dirs, snaps...)
	for _, d := range dirs {
		if p := filepath.Join(d, "tokenizer.json"); fileExists(p) {
			return p, nil
		}
	}
	return "", errors.New("tokenizer.json not found for " + model)
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// GetCounter returns the model's tokenizer, or the heuristic if unavailable.
func GetCounter(model string) Counter {
	if model == "" || model == "heuristic" {
		return Heuristic
	}
	path, err := FindTokenizer(model)
	if err != nil {
		return Heuristic
	}
	c, err := NewHFCounter(path)
	if err != nil {
		return Heuristic
	}
	return c.Count
}
