package summarize

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gabriele-mastrapasqua/linklore/internal/llm"
	"github.com/gabriele-mastrapasqua/linklore/internal/llm/fake"
)

func TestSummarize_happyPath(t *testing.T) {
	fb := &fake.Backend{
		GenerateText: `{"tldr":"A clean primer on local-first software design.","tags":["local-first","software","design"]}`,
	}
	s := New(fb, Default())
	r, err := s.Summarize(context.Background(), "Local-first matters", "long body...", nil)
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if !strings.HasPrefix(r.TLDR, "A clean primer") {
		t.Errorf("tldr = %q", r.TLDR)
	}
	if len(r.Tags) != 3 || r.Tags[0] != "local-first" {
		t.Errorf("tags = %v", r.Tags)
	}
	if fb.Calls() != 1 {
		t.Errorf("expected 1 call, got %d", fb.Calls())
	}
}

func TestSummarize_extractsJSONFromCodeFences(t *testing.T) {
	fb := &fake.Backend{
		GenerateText: "Sure, here you go:\n```json\n{\"tldr\":\"hi\",\"tags\":[\"a\"]}\n```",
	}
	r, err := New(fb, Default()).Summarize(context.Background(), "t", "b", nil)
	if err != nil {
		t.Fatalf("expected recovery from fences: %v", err)
	}
	if r.TLDR != "hi" {
		t.Errorf("tldr = %q", r.TLDR)
	}
}

func TestSummarize_recoversFromTrailingProse(t *testing.T) {
	fb := &fake.Backend{
		GenerateText: `Here's the JSON: {"tldr":"x","tags":["go"]}. Hope that helps!`,
	}
	r, err := New(fb, Default()).Summarize(context.Background(), "t", "b", nil)
	if err != nil {
		t.Fatalf("expected regex recovery: %v", err)
	}
	if r.TLDR != "x" {
		t.Errorf("tldr = %q", r.TLDR)
	}
}

func TestSummarize_emptyResponseFails(t *testing.T) {
	fb := &fake.Backend{GenerateText: " "}
	if _, err := New(fb, Default()).Summarize(context.Background(), "t", "b", nil); err == nil {
		t.Error("expected error")
	}
}

func TestSummarize_missingTldrFails(t *testing.T) {
	fb := &fake.Backend{GenerateText: `{"tags":["x"]}`}
	if _, err := New(fb, Default()).Summarize(context.Background(), "t", "b", nil); err == nil {
		t.Error("expected error")
	}
}

func TestSummarize_backendErrorBubblesUp(t *testing.T) {
	want := errors.New("net/down")
	fb := &fake.Backend{GenerateError: want}
	if _, err := New(fb, Default()).Summarize(context.Background(), "t", "b", nil); err == nil {
		t.Error("expected error")
	}
}

func TestSummarize_normalisesTagsViaTagsPackage(t *testing.T) {
	fb := &fake.Backend{
		GenerateText: `{"tldr":"x","tags":["Go", "go!", "Machine Learning", "Tags"]}`,
	}
	r, err := New(fb, Default()).Summarize(context.Background(), "t", "b", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"go", "machine-learning", "tag"}
	if len(r.Tags) != len(want) {
		t.Fatalf("got %v", r.Tags)
	}
	for i := range want {
		if r.Tags[i] != want[i] {
			t.Errorf("tag[%d] = %q want %q", i, r.Tags[i], want[i])
		}
	}
}

func TestSummarize_capPerLinkApplied(t *testing.T) {
	fb := &fake.Backend{
		GenerateText: `{"tldr":"x","tags":["a","b","c","d","e","f","g"]}`,
	}
	cfg := Default()
	cfg.Tags.MaxPerLink = 3
	r, err := New(fb, cfg).Summarize(context.Background(), "t", "b", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Tags) != 3 {
		t.Errorf("len = %d", len(r.Tags))
	}
}

func TestSummarize_existingTagsInjectedInPrompt(t *testing.T) {
	rec := &recordingBackend{out: `{"tldr":"x","tags":[]}`}
	s := New(rec, Default())
	if _, err := s.Summarize(context.Background(), "T", "B", []string{"go", "rust", "ml"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rec.lastPrompt, "go, rust, ml") {
		t.Errorf("existing tags not injected: %s", rec.lastPrompt)
	}
	if !strings.Contains(rec.lastPrompt, "REUSING") {
		t.Errorf("reuse instruction missing")
	}
}

func TestSummarize_retriesOnGarbageThenSucceeds(t *testing.T) {
	rec := &sequenceBackend{
		responses: []string{
			"I don't know what to say",                // attempt 1: not JSON
			`{"tldr":"recovered","tags":["recover"]}`, // attempt 2: valid
		},
	}
	r, err := New(rec, Default()).Summarize(context.Background(), "t", "b", nil)
	if err != nil {
		t.Fatalf("expected recovery: %v", err)
	}
	if r.TLDR != "recovered" {
		t.Errorf("tldr = %q", r.TLDR)
	}
	if rec.calls.Load() != 2 {
		t.Errorf("expected 2 calls, got %d", rec.calls.Load())
	}
}

func TestSummarize_exhaustsRetries(t *testing.T) {
	rec := &sequenceBackend{responses: []string{"nope", "still nope", "really not json"}}
	cfg := Default()
	cfg.MaxRetries = 2 // 3 total attempts, all bad
	if _, err := New(rec, cfg).Summarize(context.Background(), "t", "b", nil); err == nil {
		t.Error("expected exhaustion error")
	}
	if rec.calls.Load() != 3 {
		t.Errorf("expected 3 calls, got %d", rec.calls.Load())
	}
}

func TestTruncateBody_headTailSplit(t *testing.T) {
	body := strings.Repeat("a", 100) + strings.Repeat("z", 100)
	out := truncateBody(body, 50)
	if !strings.Contains(out, "truncated") {
		t.Errorf("missing marker: %q", out)
	}
	if !strings.HasPrefix(out, "aaaa") {
		t.Errorf("head missing")
	}
	if !strings.HasSuffix(out, "zzzz") {
		t.Errorf("tail missing")
	}
}

func TestTruncateBody_passthroughWhenSmall(t *testing.T) {
	if truncateBody("short", 100) != "short" {
		t.Errorf("expected passthrough")
	}
}

// ---- minimal llm.Backend adapters used only by tests ----

// recordingBackend captures the last prompt sent and returns `out` every time.
type recordingBackend struct {
	lastPrompt string
	out        string
}

func (r *recordingBackend) Generate(_ context.Context, prompt string, _ *llm.GenerateOptions) (*llm.GenerateResult, error) {
	r.lastPrompt = prompt
	return &llm.GenerateResult{Text: r.out}, nil
}
func (r *recordingBackend) GenerateStream(_ context.Context, _ string, _ *llm.GenerateOptions) (<-chan llm.StreamChunk, error) {
	panic("not used")
}
func (r *recordingBackend) Embed(_ context.Context, _ []string, _ *llm.EmbedOptions) (*llm.EmbedResult, error) {
	panic("not used")
}

// sequenceBackend cycles through canned responses, one per Generate call.
// Used to exercise the retry loop deterministically.
type sequenceBackend struct {
	responses []string
	calls     atomic.Int32
}

func (s *sequenceBackend) Generate(_ context.Context, _ string, _ *llm.GenerateOptions) (*llm.GenerateResult, error) {
	i := s.calls.Add(1) - 1
	if int(i) >= len(s.responses) {
		return &llm.GenerateResult{Text: "still bad"}, nil
	}
	return &llm.GenerateResult{Text: s.responses[i]}, nil
}
func (s *sequenceBackend) GenerateStream(_ context.Context, _ string, _ *llm.GenerateOptions) (<-chan llm.StreamChunk, error) {
	panic("not used")
}
func (s *sequenceBackend) Embed(_ context.Context, _ []string, _ *llm.EmbedOptions) (*llm.EmbedResult, error) {
	panic("not used")
}
