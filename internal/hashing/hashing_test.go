package hashing

import "testing"

// Golden values, cross-checked with an independent sha256 (hashlib).
func TestGolden(t *testing.T) {
	cases := []struct{ got, want string }{
		{DocumentID("Inventory.md"), "86a82f5fc5c9d0d6"},
		{ChunkID("Inventory.md", "Inventory > Closing an inventory", 0, 0), "f6d2ef1ea8cc3022"},
		{ChunkID("Processes/Café-walkthrough.md", "Walkthrough > Steps", 2, 1), "5bd3114fd6c30adf"},
		{EmbeddingKey("intfloat/multilingual-e5-base", "passage: Document: Inventory"), "8187469d1f73fe79ebd3652250ee7875f9c29906cd5facb9e9b671d290eacc99"},
	}
	for i, c := range cases {
		if c.got != c.want {
			t.Errorf("case %d: got %s want %s", i, c.got, c.want)
		}
	}
}
