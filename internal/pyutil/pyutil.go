// Package pyutil reproduces the Python string semantics the original
// (Python) implementation used, so chunk boundaries, IDs and indexes stay
// stable across the two.
//
// Differences from the Go standard library that matter here:
//   - Python's str.isspace / regex \s also treat U+001C..U+001F as whitespace.
//   - Python's regex \w is Unicode: letters, numbers and '_'.
//   - len(str) counts code points, not bytes.
package pyutil

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
)

// Space is a regexp character-class body equivalent to Python's Unicode \s.
const Space = `\t\n\v\f\r \x{1c}-\x{1f}\x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}`

// Word is a regexp character-class body equivalent to Python's Unicode \w.
const Word = `\p{L}\p{N}_`

func IsSpace(r rune) bool { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) }

func Strip(s string) string  { return strings.TrimFunc(s, IsSpace) }
func LStrip(s string) string { return strings.TrimLeftFunc(s, IsSpace) }

// Len counts code points, like Python's len(str).
func Len(s string) int { return utf8.RuneCountInString(s) }

var folder = cases.Fold()

// Casefold approximates Python's str.casefold (full Unicode case folding).
func Casefold(s string) string { return folder.String(s) }

// Unquote mirrors urllib.parse.unquote: %XX escapes become bytes, the result is
// decoded as UTF-8 and invalid bytes become U+FFFD; malformed escapes stay as-is.
func Unquote(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var buf []byte
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]) {
			buf = append(buf, unhex(s[i+1])<<4|unhex(s[i+2]))
			i += 2
			continue
		}
		buf = append(buf, s[i])
	}
	var out strings.Builder
	for len(buf) > 0 {
		r, size := utf8.DecodeRune(buf)
		out.WriteRune(r) // RuneError for each invalid byte, like errors="replace"
		buf = buf[size:]
	}
	return out.String()
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

func unhex(c byte) byte {
	switch {
	case c <= '9':
		return c - '0'
	case c >= 'a':
		return c - 'a' + 10
	default:
		return c - 'A' + 10
	}
}

// SplitLines mirrors str.splitlines(): it breaks on \n, \r, \r\n, \v, \f,
// \x1c-\x1e, \x85, U+2028 and U+2029, and a trailing break adds no empty line.
func SplitLines(s string) []string {
	var out []string
	rs := []rune(s)
	var cur strings.Builder
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch r {
		case '\n', '\r', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
			out = append(out, cur.String())
			cur.Reset()
			if r == '\r' && i+1 < len(rs) && rs[i+1] == '\n' {
				i++
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// DecodeFile mirrors raw.decode("utf-8-sig", errors="replace"): a leading BOM
// is dropped and each invalid byte becomes U+FFFD.
func DecodeFile(raw []byte) string {
	raw = bytesTrimBOM(raw)
	if utf8.Valid(raw) {
		return string(raw)
	}
	var out strings.Builder
	for len(raw) > 0 {
		r, size := utf8.DecodeRune(raw)
		out.WriteRune(r)
		raw = raw[size:]
	}
	return out.String()
}

func bytesTrimBOM(b []byte) []byte {
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		return b[3:]
	}
	return b
}

// Lower mirrors Python's str.lower(): unicode.ToLower plus the two
// unconditional/contextual SpecialCasing rules CPython applies:
// U+0130 'İ' -> "i̇", and capital sigma -> final 'ς' at the end of a word.
func Lower(s string) string {
	rs := []rune(s)
	var b strings.Builder
	b.Grow(len(s))
	for i, r := range rs {
		switch {
		case r == 'İ':
			b.WriteString("i̇")
		case r == 'Σ' && finalSigma(rs, i):
			b.WriteRune('ς')
		default:
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// finalSigma: preceded by a cased letter and not followed by one (case-ignorable
// characters such as combining marks and apostrophes are skipped).
func finalSigma(rs []rune, i int) bool {
	ignorable := func(r rune) bool {
		return unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf, unicode.Lm, unicode.Sk) || r == '\'' || r == '.' || r == ':' || r == '·'
	}
	cased := func(r rune) bool { return unicode.IsUpper(r) || unicode.IsLower(r) || unicode.IsTitle(r) }
	before := false
	for j := i - 1; j >= 0; j-- {
		if ignorable(rs[j]) {
			continue
		}
		before = cased(rs[j])
		break
	}
	if !before {
		return false
	}
	for j := i + 1; j < len(rs); j++ {
		if ignorable(rs[j]) {
			continue
		}
		return !cased(rs[j])
	}
	return true
}
