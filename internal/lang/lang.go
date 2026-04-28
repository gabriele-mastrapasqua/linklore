// Package lang detects the natural language of a Markdown body using a
// tiny stop-word / character-frequency heuristic.
//
// Why not lingua-go: lingua's embedded n-gram tables add ~120 MB to the
// binary, which is at odds with linklore's "small and fast" goal. Stop-word
// counting is good enough for a UI hint badge and adds zero binary weight.
//
// Supported codes: en, it, fr, es, de, pt, nl. Returns "" when no clear
// majority is found.
package lang

import (
	"sort"
	"strings"
	"unicode"
)

// stopWords[code] = a small set of frequent function words for that language.
// Selected to avoid cross-language overlap (e.g. "il" is shared by it/fr,
// "de" is shared by es/de, so we pick more discriminative words).
// Each list is hand-tuned to maximise discriminative power, not coverage.
// We deliberately drop tokens that appear across multiple languages
// (e.g. "de" is too universal; it gives nl a free win against fr/pt/es).
var stopWords = map[string][]string{
	"en": {"the", "and", "is", "in", "to", "of", "that", "for", "with", "this", "are", "was", "be", "have", "from", "you"},
	"it": {"il", "lo", "la", "che", "non", "una", "uno", "sono", "questo", "questa", "anche", "perché", "ma", "essere", "molto"},
	"fr": {"le", "les", "une", "des", "est", "pas", "que", "qui", "dans", "pour", "avec", "ceci", "cette", "ne", "et", "sont", "être"},
	"es": {"el", "los", "las", "que", "una", "esto", "para", "con", "pero", "más", "está", "son", "muy", "porque"},
	"de": {"der", "und", "ist", "nicht", "ein", "eine", "auch", "sich", "mit", "auf", "von", "den", "dem", "wir"},
	"pt": {"os", "as", "uma", "está", "são", "mais", "pelo", "pela", "porque", "muito", "também"},
	"nl": {"het", "een", "dat", "niet", "voor", "deze", "ook", "maar", "zijn", "haar", "worden"},
}

// Detect returns the most likely ISO 639-1 code, or "" when the input is
// empty or no language has a clear plurality of stop-word hits.
func Detect(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	const maxBytes = 4 << 10
	if len(text) > maxBytes {
		text = text[:maxBytes]
	}

	tokens := tokenize(text)
	if len(tokens) < 5 {
		return ""
	}

	scores := map[string]int{}
	for code, words := range stopWords {
		set := make(map[string]struct{}, len(words))
		for _, w := range words {
			set[w] = struct{}{}
		}
		for _, t := range tokens {
			if _, ok := set[t]; ok {
				scores[code]++
			}
		}
	}

	type pair struct {
		code string
		n    int
	}
	ranked := make([]pair, 0, len(scores))
	for c, n := range scores {
		ranked = append(ranked, pair{c, n})
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].n > ranked[j].n })

	if len(ranked) == 0 || ranked[0].n == 0 {
		return ""
	}
	// Require a clear margin: the winner must beat the runner-up by at
	// least 30%, otherwise we abstain. Avoids labelling a 50/50 mix.
	if len(ranked) >= 2 && ranked[1].n > 0 {
		if float64(ranked[0].n) < 1.3*float64(ranked[1].n) {
			return ""
		}
	}
	return ranked[0].code
}

// tokenize lowercases and splits on non-letter runes.
func tokenize(s string) []string {
	var out []string
	var b strings.Builder
	flush := func() {
		if b.Len() > 0 {
			out = append(out, b.String())
			b.Reset()
		}
	}
	for _, r := range s {
		if unicode.IsLetter(r) {
			b.WriteRune(unicode.ToLower(r))
		} else {
			flush()
		}
	}
	flush()
	return out
}
