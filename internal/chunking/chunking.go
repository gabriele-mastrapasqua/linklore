// Package chunking splits a Markdown body into windows suitable for
// embedding and RAG retrieval. The strategy is intentionally simple:
//
//  1. Split paragraphs on blank lines, keeping headings as paragraph starts.
//  2. Pack paragraphs greedily up to TargetTokens (whitespace tokens).
//  3. When a chunk closes, prepend OverlapTokens trailing tokens to the
//     next chunk so context bleeds across boundaries.
//  4. Drop chunks shorter than MinTokens — tiny tail chunks add noise.
//
// "Tokens" here are whitespace-delimited words. Real tokenisation is the
// LLM's job; over-counting by a small factor is fine for window sizing.
package chunking

import "strings"

// Config holds the knobs from configs/config.yaml's chunking: section.
type Config struct {
	TargetTokens  int
	OverlapTokens int
	MinTokens     int
}

// Default mirrors config.Default().Chunking.
func Default() Config {
	return Config{TargetTokens: 800, OverlapTokens: 100, MinTokens: 40}
}

// Chunk splits markdown into chunks per the config. A document shorter than
// TargetTokens collapses to one chunk regardless of MinTokens — we never
// want to lose a tweet-sized link entirely.
func Chunk(md string, cfg Config) []string {
	cfg = sanitize(cfg)
	md = strings.TrimSpace(md)
	if md == "" {
		return nil
	}
	totalTokens := countTokens(md)
	if totalTokens <= cfg.TargetTokens {
		return []string{md}
	}

	paragraphs := splitParagraphs(md)
	out := make([]string, 0, totalTokens/cfg.TargetTokens+1)

	var pending []string // current chunk's paragraphs
	pendingTokens := 0
	for _, p := range paragraphs {
		t := countTokens(p)

		// A single paragraph longer than TargetTokens: flush whatever's
		// pending, then split the long paragraph word-by-word.
		if t > cfg.TargetTokens {
			if pendingTokens > 0 {
				out = appendIfBigEnough(out, joinParagraphs(pending), cfg.MinTokens)
				pending, pendingTokens = nil, 0
			}
			for _, slice := range splitOversizedParagraph(p, cfg) {
				out = appendIfBigEnough(out, slice, cfg.MinTokens)
			}
			continue
		}

		if pendingTokens+t > cfg.TargetTokens && pendingTokens > 0 {
			chunk := joinParagraphs(pending)
			out = appendIfBigEnough(out, chunk, cfg.MinTokens)
			// Seed next pending with overlap tail of the chunk just closed.
			tail := tailTokens(chunk, cfg.OverlapTokens)
			pending = nil
			pendingTokens = 0
			if tail != "" {
				pending = append(pending, tail)
				pendingTokens = countTokens(tail)
			}
		}
		pending = append(pending, p)
		pendingTokens += t
	}
	if pendingTokens > 0 {
		out = appendIfBigEnough(out, joinParagraphs(pending), cfg.MinTokens)
	}

	// Edge case: doc was longer than TargetTokens but every chunk filtered
	// below MinTokens. Better to return one big chunk than zero.
	if len(out) == 0 {
		return []string{md}
	}
	return out
}

func sanitize(cfg Config) Config {
	if cfg.TargetTokens <= 0 {
		cfg.TargetTokens = 800
	}
	if cfg.OverlapTokens < 0 || cfg.OverlapTokens >= cfg.TargetTokens {
		cfg.OverlapTokens = 100
	}
	if cfg.MinTokens < 0 {
		cfg.MinTokens = 0
	}
	return cfg
}

// splitParagraphs splits on blank-line runs. Lines that start with "#" are
// treated as their own paragraph so a heading attaches to the section that
// follows it (it ends up as paragraph N, body as paragraph N+1, packed
// together by the greedy loop in Chunk).
func splitParagraphs(md string) []string {
	lines := strings.Split(md, "\n")
	var paras []string
	var cur []string
	flush := func() {
		if len(cur) == 0 {
			return
		}
		paras = append(paras, strings.TrimRight(strings.Join(cur, "\n"), "\n"))
		cur = nil
	}
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			flush()
			continue
		}
		cur = append(cur, l)
	}
	flush()
	return paras
}

func joinParagraphs(ps []string) string {
	return strings.Join(ps, "\n\n")
}

func countTokens(s string) int {
	return len(strings.Fields(s))
}

func tailTokens(s string, n int) string {
	if n <= 0 {
		return ""
	}
	fields := strings.Fields(s)
	if len(fields) <= n {
		return s
	}
	return strings.Join(fields[len(fields)-n:], " ")
}

// splitOversizedParagraph breaks a single paragraph that exceeds TargetTokens
// into windows of TargetTokens with OverlapTokens overlap, word-by-word.
func splitOversizedParagraph(p string, cfg Config) []string {
	fields := strings.Fields(p)
	step := cfg.TargetTokens - cfg.OverlapTokens
	if step <= 0 {
		step = cfg.TargetTokens
	}
	var out []string
	for start := 0; start < len(fields); start += step {
		end := min(start+cfg.TargetTokens, len(fields))
		out = append(out, strings.Join(fields[start:end], " "))
		if end == len(fields) {
			break
		}
	}
	return out
}

func appendIfBigEnough(out []string, chunk string, minTok int) []string {
	if minTok > 0 && countTokens(chunk) < minTok {
		return out
	}
	return append(out, chunk)
}
