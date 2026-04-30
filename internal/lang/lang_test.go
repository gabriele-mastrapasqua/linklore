package lang

import (
	"strings"
	"testing"
)

func TestDetect_basic(t *testing.T) {
	cases := map[string]string{
		"":    "",
		"   ": "",
		"This is a clearly English passage about software engineering and ownership semantics.": "en",
		"Questo è un testo chiaramente in italiano che parla di programmazione e di tipi.":      "it",
		"Ceci est un passage clairement en français à propos de programmation et de types.":     "fr",
	}
	for in, want := range cases {
		if got := Detect(in); got != want {
			t.Errorf("Detect(%q) = %q, want %q", trunc(in), got, want)
		}
	}
}

func TestDetect_truncatesLongInput(t *testing.T) {
	// 100 KiB of English nonsense — should still answer fast and return "en".
	var b strings.Builder
	for range 5000 {
		b.WriteString("the quick brown fox jumps over the lazy dog ")
	}
	body := b.String()
	if got := Detect(body); got != "en" {
		t.Errorf("got %q on long input", got)
	}
}

func trunc(s string) string {
	if len(s) > 40 {
		return s[:40] + "…"
	}
	return s
}
