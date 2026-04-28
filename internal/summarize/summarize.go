// Package summarize wraps an llm.Backend with linklore-specific prompt logic
// for producing per-link TL;DRs and auto-tag suggestions in a single call.
//
// Output contract (JSON):
//
//	{
//	  "tldr": "<= 60 words on what this is and why it matters",
//	  "tags": ["slug-1", "slug-2", ...]   // up to 5
//	}
//
// We bias the LLM towards reusing existing tag slugs by injecting the top-N
// busy slugs into the prompt with an explicit "prefer reuse" instruction.
// Final slugifying / fuzzy reuse / cap enforcement happens in internal/tags
// — this package only parses what the model returned.
package summarize

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/gabrielemastrapasqua/linklore/internal/llm"
	"github.com/gabrielemastrapasqua/linklore/internal/tags"
)

// Config holds the static knobs.
type Config struct {
	// MaxRetries is how many extra attempts we make when the model returns
	// non-JSON or JSON with the wrong shape. Default 2 ⇒ 3 total tries.
	MaxRetries int
	// MaxBodyChars caps the content sent to the LLM. Past this, head + tail
	// split keeps both intro and conclusion in the context window.
	MaxBodyChars int
	// Tags carries the normalisation knobs.
	Tags tags.Config
}

// Default returns sane defaults: 2 retries, 16k char cap on the body.
func Default() Config {
	return Config{MaxRetries: 2, MaxBodyChars: 16000, Tags: tags.Default()}
}

// Result is what callers consume. Tags are already slugified+deduped.
type Result struct {
	TLDR string
	Tags []string
}

// Summarizer is the public type — keeps a Backend reference and the config.
type Summarizer struct {
	backend llm.Backend
	cfg     Config
}

func New(b llm.Backend, cfg Config) *Summarizer {
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 0
	}
	if cfg.MaxBodyChars <= 0 {
		cfg.MaxBodyChars = 16000
	}
	if cfg.Tags.MaxPerLink <= 0 {
		cfg.Tags = tags.Default()
	}
	return &Summarizer{backend: b, cfg: cfg}
}

// Summarize calls the LLM up to MaxRetries+1 times, normalises the resulting
// tags against existingTagSlugs, and returns the final Result.
func (s *Summarizer) Summarize(ctx context.Context, title, contentMD string, existingTagSlugs []string) (*Result, error) {
	body := truncateBody(contentMD, s.cfg.MaxBodyChars)
	prompt := buildPrompt(title, body, existingTagSlugs, s.cfg.Tags.MaxPerLink)

	var lastErr error
	for attempt := 0; attempt <= s.cfg.MaxRetries; attempt++ {
		res, err := s.backend.Generate(ctx, prompt, &llm.GenerateOptions{Temperature: 0.2})
		if err != nil {
			// Network errors aren't worth retrying — surface and let the
			// worker schedule its own backoff.
			return nil, fmt.Errorf("summarize: backend call: %w", err)
		}
		parsed, perr := parseJSON(res.Text)
		if perr == nil {
			parsed.Tags = tags.Normalize(parsed.Tags, existingTagSlugs, s.cfg.Tags)
			return parsed, nil
		}
		lastErr = perr
		// Tighten the prompt for the retry: explicit "respond with ONLY the JSON".
		prompt = buildRetryPrompt(prompt, res.Text)
	}
	return nil, fmt.Errorf("summarize: exhausted retries: %w", lastErr)
}

// truncateBody applies a head+tail split so we keep both intro context and
// the conclusion under MaxBodyChars. Whole-doc passthrough below the cap.
func truncateBody(body string, max int) string {
	if max <= 0 || len(body) <= max {
		return body
	}
	half := max / 2
	return body[:half] + "\n\n[…truncated…]\n\n" + body[len(body)-half:]
}

func buildPrompt(title, body string, existing []string, maxTags int) string {
	var b strings.Builder
	b.WriteString("You are a precise summariser for a personal link library.\n")
	b.WriteString("Read the document below and respond with ONE valid JSON object — no prose, no code fences.\n\n")
	b.WriteString("Schema:\n")
	b.WriteString(`{"tldr": "<= 60 words explaining what this is and why it matters", "tags": ["slug-1", "slug-2", ...]}`)
	b.WriteString("\n\nRules:\n")
	b.WriteString("- tldr: at most 60 words, plain prose, no markdown, no quotes around it.\n")
	b.WriteString(fmt.Sprintf("- tags: between 1 and %d short topical labels (lowercase, hyphenated).\n", maxTags))
	if len(existing) > 0 {
		b.WriteString("- Prefer REUSING the existing tag slugs below over inventing near-duplicates:\n  ")
		// Cap injected list so the prompt doesn't blow up on very large taxonomies.
		const maxInject = 50
		if len(existing) > maxInject {
			existing = existing[:maxInject]
		}
		b.WriteString(strings.Join(existing, ", "))
		b.WriteString("\n")
	}
	b.WriteString("\nTitle: ")
	b.WriteString(title)
	b.WriteString("\n\nDocument:\n")
	b.WriteString(body)
	b.WriteString("\n\nJSON:")
	return b.String()
}

func buildRetryPrompt(prev, badResponse string) string {
	return prev + "\n\nYour previous response was not valid JSON:\n" +
		strings.TrimSpace(badResponse) +
		"\n\nReturn ONLY the JSON object that matches the schema."
}

// jsonObjectRe finds the first {...} block in the response. Used to recover
// from chatty models that wrap JSON in commentary or code fences.
var jsonObjectRe = regexp.MustCompile(`(?s)\{.*\}`)

type rawResult struct {
	TLDR string   `json:"tldr"`
	Tags []string `json:"tags"`
}

func parseJSON(s string) (*Result, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("empty response")
	}
	// Strip ```json fences if the model insists on them.
	if strings.HasPrefix(s, "```") {
		if i := strings.Index(s, "\n"); i > 0 {
			s = s[i+1:]
		}
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	}
	candidate := s
	if !strings.HasPrefix(strings.TrimSpace(candidate), "{") {
		match := jsonObjectRe.FindString(s)
		if match == "" {
			return nil, fmt.Errorf("no JSON object found in response")
		}
		candidate = match
	}
	var raw rawResult
	if err := json.Unmarshal([]byte(candidate), &raw); err != nil {
		return nil, fmt.Errorf("decode JSON: %w", err)
	}
	if strings.TrimSpace(raw.TLDR) == "" {
		return nil, errors.New("missing tldr field")
	}
	return &Result{TLDR: strings.TrimSpace(raw.TLDR), Tags: raw.Tags}, nil
}
