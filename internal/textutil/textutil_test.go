package textutil

import (
	"encoding/json"
	"os"
	"testing"
)

func TestLowerUnicode(t *testing.T) {
	data, err := os.ReadFile("../../testdata/lower_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases map[string]string
	json.Unmarshal(data, &cases)
	for in, want := range cases {
		if got := Lower(in); got != want {
			t.Errorf("Lower(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStripTreatsUnitSeparatorAsSpace(t *testing.T) {
	if Strip("\x1f a  ") != "a" {
		t.Fatal("strip")
	}
}
