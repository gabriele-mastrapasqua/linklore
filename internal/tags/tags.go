// Package tags handles auto-tag normalisation for linklore.
//
// Goals (matching CLAUDE.md / PLAN.md):
//   - Slugify raw LLM output: lowercase, ASCII-only, single dashes.
//   - Drop trailing 's' lemma so "tags"/"tag" merge into one slug.
//   - Reuse near-duplicate existing slugs (Levenshtein ≤ ReuseDistance).
//   - Cap per-link to MaxPerLink suggestions.
//   - Hard cap on globally active tags: when over the cap, summarize() may
//     still propose new tags but the storage layer / UI surfaces them as
//     "needs merge" instead of silently exploding the taxonomy.
package tags

import (
	"strings"
	"unicode"
)

// Config controls the normalisation knobs. Mirrors config.Tags.
type Config struct {
	MaxPerLink    int
	ReuseDistance int // 0 disables fuzzy reuse; 1 catches near-misses cheaply
}

// Default returns the same defaults as config.Default().Tags.
func Default() Config {
	return Config{MaxPerLink: 5, ReuseDistance: 1}
}

// Slugify turns a free-form tag name into a normalised slug:
//   - lowercase
//   - non-alnum runs collapsed to "-"
//   - leading/trailing dashes stripped
//   - trailing 's' removed (cheap lemma) when length > 3
//
// Returns "" if the input has no alphanumeric characters at all — the caller
// must skip empty slugs rather than silently storing a "" tag.
func Slugify(name string) string {
	var b strings.Builder
	prevDash := true
	for _, r := range strings.TrimSpace(name) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(unicode.ToLower(r))
			prevDash = false
		case !prevDash:
			b.WriteByte('-')
			prevDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 3 && strings.HasSuffix(out, "s") {
		out = out[:len(out)-1]
	}
	return out
}

// Normalize takes raw tag names and existing slugs, returns a deduped,
// reused-where-possible, capped slice of slugs ready to upsert. Order is
// preserved (insertion order, dedup-by-first-occurrence).
//
// existing should be the corpus of slugs already in storage (passing the
// "top N" set is fine — fuzzy reuse only matters against the busy ones).
func Normalize(raw []string, existing []string, cfg Config) []string {
	if cfg.MaxPerLink <= 0 {
		cfg.MaxPerLink = 5
	}
	exMap := make(map[string]struct{}, len(existing))
	for _, s := range existing {
		exMap[s] = struct{}{}
	}
	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, cfg.MaxPerLink)

	for _, r := range raw {
		s := Slugify(r)
		if s == "" {
			continue
		}
		// Fuzzy reuse: snap to an existing slug within ReuseDistance.
		if cfg.ReuseDistance > 0 {
			if hit, ok := nearest(s, existing, cfg.ReuseDistance); ok {
				s = hit
			}
		} else if _, ok := exMap[s]; ok {
			// exact match counts even when fuzzy is off
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
		if len(out) >= cfg.MaxPerLink {
			break
		}
	}
	return out
}

// nearest returns the existing slug with smallest edit distance ≤ maxDist.
// Skips comparisons whose length differs by more than maxDist (cheap prune).
func nearest(s string, existing []string, maxDist int) (string, bool) {
	best := ""
	bestD := maxDist + 1
	for _, ex := range existing {
		if abs(len(ex)-len(s)) > maxDist {
			continue
		}
		d := levenshtein(s, ex, maxDist)
		if d < bestD {
			bestD = d
			best = ex
		}
	}
	if bestD <= maxDist {
		return best, true
	}
	return "", false
}

// levenshtein with early-exit when the running min exceeds maxDist.
// Returns maxDist+1 when above the budget (caller treats it as "too far").
func levenshtein(a, b string, maxDist int) int {
	if a == b {
		return 0
	}
	la, lb := len(a), len(b)
	if abs(la-lb) > maxDist {
		return maxDist + 1
	}
	prev := make([]int, lb+1)
	curr := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		curr[0] = i
		rowMin := curr[0]
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min3(curr[j-1]+1, prev[j]+1, prev[j-1]+cost)
			if curr[j] < rowMin {
				rowMin = curr[j]
			}
		}
		if rowMin > maxDist {
			return maxDist + 1
		}
		prev, curr = curr, prev
	}
	return prev[lb]
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}
