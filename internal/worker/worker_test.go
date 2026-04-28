package worker

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gabrielemastrapasqua/linklore/internal/config"
	"github.com/gabrielemastrapasqua/linklore/internal/extract"
	"github.com/gabrielemastrapasqua/linklore/internal/llm"
	"github.com/gabrielemastrapasqua/linklore/internal/llm/fake"
	"github.com/gabrielemastrapasqua/linklore/internal/storage"
)

const fixtureHTML = `<!doctype html><html>
<head>
<title>Fixture article — local-first stuff</title>
<meta property="og:image" content="https://x/cover.png">
<meta name="description" content="A meaningful description.">
</head>
<body>
<article>
<h1>Fixture article</h1>
<p>This is the body of a fixture article that has more than enough content to satisfy readability and produce at least one chunk that the worker will then embed and summarise. The whole local-first idea is that your data lives next to you. Lorem ipsum dolor sit amet consectetur adipiscing elit sed do eiusmod tempor.</p>
<p>Second paragraph adds even more substance: ut enim ad minim veniam quis nostrud exercitation ullamco laboris nisi ut aliquip ex ea commodo consequat. Duis aute irure dolor in reprehenderit in voluptate velit esse cillum.</p>
</article>
</body></html>`

// stubFetcher serves a canned response without going to the network.
type stubFetcher struct {
	body string
	err  error
}

func (s *stubFetcher) Fetch(_ context.Context, _ string) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	return s.body, nil
}

// flakyFetcher fails the first N calls then succeeds.
type flakyFetcher struct {
	body string
	fail int32
}

func (f *flakyFetcher) Fetch(_ context.Context, _ string) (string, error) {
	if atomic.AddInt32(&f.fail, -1) >= 0 {
		return "", errors.New("network down")
	}
	return f.body, nil
}

func newWorker(t *testing.T, fetch Fetcher, backend llm.Backend) (*Worker, *storage.Store, int64) {
	t.Helper()
	st, err := storage.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	col, _ := st.CreateCollection(context.Background(), "c", "C", "")
	cfg := config.Default()
	cfg.Worker.Concurrency = 2
	cfg.Worker.EmbedBatchSize = 8
	cfg.Chunking.TargetTokens = 50
	cfg.Chunking.OverlapTokens = 10
	cfg.Chunking.MinTokens = 5
	w := New(st, backend, fetch, cfg, Options{Logger: log.New(io.Discard, "", 0)})
	return w, st, col.ID
}

func TestWorker_happyPath_pendingToSummarized(t *testing.T) {
	backend := &fake.Backend{
		GenerateText: `{"tldr":"A fixture about local-first software.","tags":["local-first","testing"]}`,
		EmbedDim:     16,
	}
	w, st, colID := newWorker(t, &stubFetcher{body: fixtureHTML}, backend)
	l, _ := st.CreateLink(context.Background(), colID, "https://example.com/x")

	if err := w.tick(context.Background()); err != nil {
		t.Fatal(err)
	}

	got, _ := st.GetLink(context.Background(), l.ID)
	if got.Status != storage.StatusSummarized {
		t.Errorf("status = %q", got.Status)
	}
	if got.Title == "" || !strings.Contains(strings.ToLower(got.Title), "fixture") {
		t.Errorf("title not extracted: %q", got.Title)
	}
	if got.Summary == "" {
		t.Errorf("summary missing")
	}

	chunks, _ := st.ListChunksByLink(context.Background(), l.ID)
	if len(chunks) == 0 {
		t.Fatal("no chunks")
	}
	for _, c := range chunks {
		if len(c.Embedding) == 0 {
			t.Errorf("chunk %d has no embedding", c.ID)
		}
	}

	tags, _ := st.ListTagsByLink(context.Background(), l.ID)
	if len(tags) == 0 {
		t.Errorf("no auto tags attached")
	}
}

func TestWorker_fetchFailureMarksFailed(t *testing.T) {
	backend := &fake.Backend{}
	w, st, colID := newWorker(t, &stubFetcher{err: errors.New("network gone")}, backend)
	l, _ := st.CreateLink(context.Background(), colID, "https://example.com/x")

	_ = w.tick(context.Background())
	got, _ := st.GetLink(context.Background(), l.ID)
	if got.Status != storage.StatusFailed {
		t.Errorf("status = %q", got.Status)
	}
	if !strings.Contains(got.FetchError, "network gone") {
		t.Errorf("fetch_error = %q", got.FetchError)
	}
}

func TestWorker_embedFailureLeavesAtFetched(t *testing.T) {
	backend := &fake.Backend{
		// Generate works (canned JSON), but Embed fails — simulate by
		// wrapping the fake with a custom backend that errs on Embed.
	}
	br := &embedFailingBackend{inner: backend}
	w, st, colID := newWorker(t, &stubFetcher{body: fixtureHTML}, br)
	l, _ := st.CreateLink(context.Background(), colID, "https://example.com/x")

	_ = w.tick(context.Background())
	got, _ := st.GetLink(context.Background(), l.ID)
	// Extraction succeeded so status flipped to fetched; embed failed so
	// it didn't progress to summarized. Worker will retry next tick.
	if got.Status != storage.StatusFetched {
		t.Errorf("status = %q (expected stuck at fetched)", got.Status)
	}
}

func TestWorker_recoversFromTransientFetch(t *testing.T) {
	backend := &fake.Backend{
		GenerateText: `{"tldr":"ok","tags":["x"]}`,
		EmbedDim:     8,
	}
	flaky := &flakyFetcher{body: fixtureHTML, fail: 1}
	w, st, colID := newWorker(t, flaky, backend)
	l, _ := st.CreateLink(context.Background(), colID, "https://example.com/x")

	// First tick: fetch fails, link goes to failed.
	_ = w.tick(context.Background())
	got, _ := st.GetLink(context.Background(), l.ID)
	if got.Status != storage.StatusFailed {
		t.Errorf("first tick status = %q", got.Status)
	}

	// Manual retry via ProcessOne (simulates UI "refetch" button).
	if err := w.ProcessOne(context.Background(), l.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetLink(context.Background(), l.ID)
	if got.Status != storage.StatusSummarized {
		t.Errorf("after retry status = %q", got.Status)
	}
}

func TestWorker_endToEnd_fromHTTPFetcher(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(fixtureHTML))
	}))
	defer srv.Close()

	backend := &fake.Backend{
		GenerateText: `{"tldr":"e2e","tags":["e2e"]}`,
		EmbedDim:     8,
	}
	w, st, colID := newWorker(t, extract.NewFetcher(2*time.Second), backend)
	l, _ := st.CreateLink(context.Background(), colID, srv.URL)

	if err := w.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetLink(context.Background(), l.ID)
	if got.Status != storage.StatusSummarized {
		t.Errorf("status = %q", got.Status)
	}
}

// embedFailingBackend wraps another backend and always errors on Embed.
type embedFailingBackend struct {
	inner llm.Backend
}

func (b *embedFailingBackend) Generate(ctx context.Context, prompt string, o *llm.GenerateOptions) (*llm.GenerateResult, error) {
	// summarise needs a valid JSON response to even reach the embed step;
	// hand-roll one regardless of what inner says.
	return &llm.GenerateResult{Text: `{"tldr":"x","tags":["x"]}`}, nil
}
func (b *embedFailingBackend) GenerateStream(ctx context.Context, _ string, _ *llm.GenerateOptions) (<-chan llm.StreamChunk, error) {
	return nil, errors.New("not used")
}
func (b *embedFailingBackend) Embed(ctx context.Context, _ []string, _ *llm.EmbedOptions) (*llm.EmbedResult, error) {
	return nil, errors.New("embed unavailable")
}
