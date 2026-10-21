// Package chunk converts parsed sections into size-bounded chunks.
// The rules are specified in docs/CHUNKING.md.
package chunk

import (
	"fmt"
	"slices"
	"strings"

	"github.com/skromesch/docgraph/internal/hashing"
	"github.com/skromesch/docgraph/internal/model"
	"github.com/skromesch/docgraph/internal/parse"
	"github.com/skromesch/docgraph/internal/pyutil"
)

const PathSep = " > "

type Options struct {
	TargetTokens     int
	MaxTokens        int
	OverlapTokens    int
	MinSectionTokens int
}

func DefaultOptions() Options { return Options{400, 480, 60, 0} }

func BuildEmbeddingText(title, path, headingPath, content string) string {
	return fmt.Sprintf("Document: %s (%s)\nSection: %s\n\n%s", title, path, headingPath, content)
}

type line struct {
	no   int // 1-based line number
	text string
}

func joinText(ls []line) string {
	parts := make([]string, len(ls))
	for i, l := range ls {
		parts[i] = l.text
	}
	return strings.Join(parts, "\n")
}

func blank(s string) bool { return pyutil.Strip(s) == "" }

// blocks splits lines into paragraph / code / table blocks.
func blocks(lines []line) [][]line {
	var out [][]line
	var cur []line
	fence := ""
	inTable := false
	flush := func() {
		for _, l := range cur {
			if !blank(l.text) {
				out = append(out, cur)
				break
			}
		}
		cur, inTable = nil, false
	}
	for _, ln := range lines {
		m := parse.FenceRE.FindStringSubmatch(ln.text)
		if fence != "" {
			cur = append(cur, ln)
			if m != nil && m[1][0] == fence[0] && len(m[1]) >= len(fence) {
				fence = ""
				flush()
			}
			continue
		}
		if m != nil {
			flush()
			fence = m[1]
			cur = append(cur, ln)
			continue
		}
		isTable := strings.HasPrefix(pyutil.LStrip(ln.text), "|")
		if inTable && !isTable {
			flush()
		}
		if isTable && !inTable {
			flush()
			inTable = true
		}
		if blank(ln.text) {
			flush()
			continue
		}
		cur = append(cur, ln)
	}
	flush()
	return out
}

type packer struct {
	count Counter
	opts  Options
}

func (p packer) tokens(ls []line) int { return p.count(joinText(ls)) }

// atomize breaks a block bigger than MaxTokens into units no bigger than MaxTokens.
func (p packer) atomize(block []line) [][]line {
	if p.tokens(block) <= p.opts.MaxTokens {
		return [][]line{block}
	}
	var units [][]line
	for _, ln := range block {
		if p.count(ln.text) <= p.opts.MaxTokens {
			units = append(units, []line{ln})
			continue
		}
		var cur []string
		for _, w := range strings.Split(ln.text, " ") {
			if len(cur) > 0 && p.count(strings.Join(append(slices.Clone(cur), w), " ")) > p.opts.TargetTokens {
				units = append(units, []line{{ln.no, strings.Join(cur, " ")}})
				cur = nil
			}
			cur = append(cur, w)
		}
		if len(cur) > 0 {
			units = append(units, []line{{ln.no, strings.Join(cur, " ")}})
		}
	}
	return units
}

func (p packer) overlapTail(piece []line) []line {
	if p.opts.OverlapTokens <= 0 {
		return nil
	}
	var tail []line
	for i := len(piece) - 1; i >= 0; i-- {
		cand := append([]line{piece[i]}, tail...)
		if p.tokens(cand) > p.opts.OverlapTokens {
			break
		}
		tail = cand
	}
	if len(tail) < len(piece) {
		return tail
	}
	return nil
}

func concat(a, b []line) []line { return append(slices.Clone(a), b...) }

func (p packer) pack(lines []line) [][]line {
	var units [][]line
	for _, b := range blocks(lines) {
		units = append(units, p.atomize(b)...)
	}
	var pieces [][]line
	var cur []line
	fresh := 0 // number of non-overlap lines in cur
	for _, unit := range units {
		if fresh > 0 && p.tokens(concat(cur, unit)) > p.opts.TargetTokens {
			pieces = append(pieces, cur)
			cur = p.overlapTail(cur)
			if p.tokens(concat(cur, unit)) > p.opts.MaxTokens {
				cur = nil
			}
			fresh = 0
		}
		if len(cur) > 0 && unit[0].no > cur[len(cur)-1].no+1 {
			cur = concat(cur, []line{{unit[0].no - 1, ""}}) // keep paragraph break
		}
		cur = concat(cur, unit)
		fresh += len(unit)
	}
	if fresh > 0 {
		pieces = append(pieces, cur)
	}
	return pieces
}

func mergeSmall(sections []parse.Section, count Counter, minTokens int) []parse.Section {
	out := make([]parse.Section, 0, len(sections))
	for _, sec := range sections {
		if minTokens > 0 && len(out) > 0 {
			prev := &out[len(out)-1]
			if sec.Level > prev.Level && len(sec.Path) >= len(prev.Path) &&
				slices.Equal(sec.Path[:len(prev.Path)], prev.Path) &&
				count(strings.Join(sec.Lines, "\n")) < minTokens {
				// sections are consecutive; only blank lines can lie between them
				gap := make([]string, max(0, sec.StartLine-prev.EndLine-1))
				prev.Lines = append(append(slices.Clone(prev.Lines), gap...), sec.Lines...)
				prev.EndLine = sec.EndLine
				prev.HasBody = prev.HasBody || sec.HasBody
				continue
			}
		}
		sec.Lines = slices.Clone(sec.Lines)
		sec.Path = slices.Clone(sec.Path)
		out = append(out, sec)
	}
	return out
}

func trimNewlines(s string) string { return strings.Trim(s, "\n") }

// Document converts a parsed document into chunks.
func Document(docPath string, parsed parse.Document, opts Options, count Counter) []model.Chunk {
	pk := packer{count, opts}
	docID := hashing.DocumentID(docPath)
	sections := mergeSmall(parsed.Sections, count, opts.MinSectionTokens)

	var chunks []model.Chunk
	emitted := map[string]string{} // heading path (\x1f-joined) -> chunk id of its first part
	occurrences := map[string]int{}

	for _, sec := range sections {
		if !sec.HasBody {
			continue
		}
		headingPath := strings.Join(sec.Path, PathSep)
		occ := occurrences[headingPath]
		occurrences[headingPath] = occ + 1

		var parentID *string
		for depth := len(sec.Path) - 1; depth > 0; depth-- {
			if id, ok := emitted[strings.Join(sec.Path[:depth], hashing.Sep)]; ok && id != "" {
				parentID = &id
				break
			}
		}

		lines := make([]line, len(sec.Lines))
		for i, t := range sec.Lines {
			lines[i] = line{sec.StartLine + i, t}
		}
		var pieces [][]line
		if count(trimNewlines(strings.Join(sec.Lines, "\n"))) <= opts.MaxTokens {
			pieces = [][]line{lines}
		} else {
			pieces = pk.pack(lines)
		}

		first := 0
		if len(pieces) > 1 {
			first = 1
		}
		for i, piece := range pieces {
			partNo := first + i
			for len(piece) > 1 && blank(piece[0].text) {
				piece = piece[1:]
			}
			for len(piece) > 1 && blank(piece[len(piece)-1].text) {
				piece = piece[:len(piece)-1]
			}
			content := trimNewlines(joinText(piece))
			cid := hashing.ChunkID(docPath, headingPath, partNo, occ)
			if partNo <= 1 {
				key := strings.Join(sec.Path, hashing.Sep)
				if _, ok := emitted[key]; !ok {
					emitted[key] = cid
				}
			}
			label := headingPath
			if partNo > 0 {
				label = fmt.Sprintf("%s (part %d/%d)", headingPath, partNo, len(pieces))
			}
			chunks = append(chunks, model.Chunk{
				ID: cid, DocumentID: docID, ParentChunkID: parentID,
				Heading: sec.Heading, HeadingPath: headingPath, HeadingLevel: sec.Level,
				Content:       content,
				EmbeddingText: BuildEmbeddingText(parsed.Title, docPath, label, content),
				ContentHash:   hashing.SHA256Text(content),
				StartLine:     piece[0].no, EndLine: piece[len(piece)-1].no,
				ChunkIndex: len(chunks), Part: partNo, TokenCount: count(content),
			})
		}
	}
	return chunks
}
