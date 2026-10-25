package index

import (
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/text/unicode/norm"

	"github.com/skromesch/docgraph/internal/config"
	"github.com/skromesch/docgraph/internal/convert"
	"github.com/skromesch/docgraph/internal/parse"
)

// Unindexed is a file (or a whole directory, with a trailing "/") under the
// indexed folder that is not in the index, and why.
type Unindexed struct{ Path, Reason string }

// ListUnindexed walks root with the same rules as Scan and reports what is
// left out: "extension", "ignored: <pattern>", "excluded dir", "pandoc missing",
// or "not indexed (failed or pending sync)" for a supported file absent from
// indexed. Excluded and ignored directories are reported once, not walked.
func ListUnindexed(root string, cfg config.Config, pandocFound bool, indexed map[string]bool) ([]Unindexed, error) {
	ic := cfg.Index
	ig := LoadIgnore(root, ic.Ignore)
	exts := map[string]bool{}
	for _, e := range ic.Extensions {
		exts[strings.ToLower(e)] = true
	}
	excluded := map[string]bool{}
	for _, d := range ic.ExcludeDirs {
		excluded[norm.NFC.String(d)] = true
	}
	var out []Unindexed
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root {
			return nil
		}
		relOS, _ := filepath.Rel(root, p)
		rel := norm.NFC.String(filepath.ToSlash(relOS))
		if d.IsDir() {
			if excluded[norm.NFC.String(d.Name())] {
				out = append(out, Unindexed{rel + "/", "excluded dir"})
				return filepath.SkipDir
			}
			if pat, ok := ig.MatchPattern(rel, true); ok {
				out = append(out, Unindexed{rel + "/", "ignored: " + pat})
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() && d.Type()&fs.ModeSymlink == 0 {
			return nil
		}
		switch pat, ignored := ig.MatchPattern(rel, false); {
		case !exts[strings.ToLower(parse.Suffix(d.Name()))]:
			out = append(out, Unindexed{rel, "extension"})
		case ignored:
			out = append(out, Unindexed{rel, "ignored: " + pat})
		case indexed[rel]:
		case convert.IsDocx(rel) && !pandocFound:
			out = append(out, Unindexed{rel, "pandoc missing"})
		default:
			out = append(out, Unindexed{rel, "not indexed (failed or pending sync)"})
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, err
}
