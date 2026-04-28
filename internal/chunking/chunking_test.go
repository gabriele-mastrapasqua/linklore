package chunking

import (
	"strings"
	"testing"
)

func TestChunk_emptyAndShortDocs(t *testing.T) {
	if got := Chunk("", Default()); got != nil {
		t.Errorf("empty -> %v", got)
	}
	short := "tiny tweet"
	got := Chunk(short, Default())
	if len(got) != 1 || got[0] != short {
		t.Errorf("short -> %v", got)
	}
}

func TestChunk_belowTargetIsOneChunk(t *testing.T) {
	body := strings.Repeat("alpha beta gamma delta ", 30) // ~120 tokens
	got := Chunk(body, Config{TargetTokens: 800, OverlapTokens: 100, MinTokens: 40})
	if len(got) != 1 {
		t.Fatalf("expected single chunk, got %d", len(got))
	}
}

func TestChunk_overflowSplitsWithOverlap(t *testing.T) {
	cfg := Config{TargetTokens: 50, OverlapTokens: 10, MinTokens: 5}
	// Three "paragraphs" of 30 tokens each — total 90 tokens, must split.
	mk := func(prefix string, n int) string {
		words := make([]string, n)
		for i := range words {
			words[i] = prefix
		}
		return strings.Join(words, " ")
	}
	body := mk("alpha", 30) + "\n\n" + mk("beta", 30) + "\n\n" + mk("gamma", 30)

	chunks := Chunk(body, cfg)
	if len(chunks) < 2 {
		t.Fatalf("expected ≥2 chunks, got %d", len(chunks))
	}
	// Overlap: tail tokens of chunk i should appear at the head of chunk i+1
	// for at least *some* boundary. Strict per-boundary checks are brittle
	// because the packer flushes at paragraph boundaries.
	overlap := false
	for i := 0; i+1 < len(chunks); i++ {
		tail := strings.Fields(chunks[i])
		if len(tail) < 5 {
			continue
		}
		needle := strings.Join(tail[len(tail)-5:], " ")
		if strings.Contains(chunks[i+1], needle) {
			overlap = true
			break
		}
	}
	if !overlap {
		t.Errorf("expected at least one overlapping pair: %v", chunks)
	}
}

func TestChunk_oversizedParagraphSplits(t *testing.T) {
	cfg := Config{TargetTokens: 20, OverlapTokens: 5, MinTokens: 3}
	// One paragraph of 100 tokens (no blank lines) → must be split.
	words := make([]string, 100)
	for i := range words {
		words[i] = "w"
	}
	body := strings.Join(words, " ")
	chunks := Chunk(body, cfg)
	if len(chunks) < 4 {
		t.Errorf("expected ≥4 chunks for oversized paragraph, got %d", len(chunks))
	}
	for i, c := range chunks {
		if n := len(strings.Fields(c)); n > cfg.TargetTokens {
			t.Errorf("chunk %d exceeds target: %d tokens", i, n)
		}
	}
}

func TestChunk_minTokensFiltering(t *testing.T) {
	// Paragraph 1 = 50 tokens, paragraph 2 = 5 tokens.
	cfg := Config{TargetTokens: 30, OverlapTokens: 0, MinTokens: 10}
	long := strings.Repeat("alpha ", 50)
	short := "tiny"
	body := long + "\n\n" + short
	chunks := Chunk(body, cfg)
	for _, c := range chunks {
		if n := len(strings.Fields(c)); n < cfg.MinTokens {
			t.Errorf("chunk below min: %q", c)
		}
	}
}

func TestChunk_minDoesNotEatEntireDoc(t *testing.T) {
	cfg := Config{TargetTokens: 5, OverlapTokens: 0, MinTokens: 100}
	// All chunks would be filtered; must fall back to one big chunk.
	body := strings.Repeat("alpha ", 40)
	chunks := Chunk(body, cfg)
	if len(chunks) != 1 {
		t.Errorf("expected fallback to single chunk, got %d", len(chunks))
	}
}

func TestChunk_headingPackedWithFollowingParagraph(t *testing.T) {
	body := "# Heading\n\nbody one body one body one\n\n## Heading 2\n\nbody two body two body two"
	got := Chunk(body, Config{TargetTokens: 100, OverlapTokens: 10, MinTokens: 1})
	if len(got) != 1 || !strings.Contains(got[0], "Heading") || !strings.Contains(got[0], "body two") {
		t.Errorf("unexpected: %v", got)
	}
}

func TestSplitParagraphs_blankLineHandling(t *testing.T) {
	in := "a\n\nb\n\n\nc\n"
	got := splitParagraphs(in)
	if strings.Join(got, "|") != "a|b|c" {
		t.Errorf("got %v", got)
	}
}

func TestTailTokens(t *testing.T) {
	if tailTokens("a b c d e", 2) != "d e" {
		t.Errorf("tail wrong")
	}
	if tailTokens("a b", 5) != "a b" {
		t.Errorf("short tail wrong")
	}
	if tailTokens("a", 0) != "" {
		t.Errorf("zero tail wrong")
	}
}
