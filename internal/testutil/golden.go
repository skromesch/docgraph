// Package testutil holds golden-file helpers shared by the package tests.
package testutil

import (
	"bytes"
	"encoding/json"
	"flag"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files from the current output")

// Update reports whether golden files are being regenerated (go test -update).
func Update() bool { return *update }

// Root is the repository root (directory of go.mod).
func Root() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// Fixtures is the small test wiki under testdata/fixtures/wiki.
func Fixtures() string { return filepath.Join(Root(), "testdata", "fixtures", "wiki") }

// GoldenPath is testdata/golden/<name>.
func GoldenPath(name string) string { return filepath.Join(Root(), "testdata", "golden", name) }

// Golden compares got with testdata/golden/<name>; with -update it writes got instead.
func Golden(t testing.TB, name string, got []byte) {
	t.Helper()
	path := GoldenPath(name)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("updated %s", name)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update to create it)", err)
	}
	if !bytes.Equal(got, want) {
		i := 0
		for i < len(got) && i < len(want) && got[i] == want[i] {
			i++
		}
		t.Errorf("%s differs at byte %d (go test -update rewrites it after an intended change):\n got  %q\n want %q",
			name, i, got[max(0, i-80):min(len(got), i+80)], want[max(0, i-80):min(len(want), i+80)])
	}
}

// GoldenJSON marshals v (indented) and compares it like Golden.
func GoldenJSON(t testing.TB, name string, v any) {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", " ")
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	Golden(t, name, buf.Bytes())
}

// Round keeps float scores stable across platforms in golden files.
func Round(x float64, digits int) float64 {
	p := math.Pow(10, float64(digits))
	return math.Round(x*p) / p
}
