package chat

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gabrielemastrapasqua/linklore/internal/embed"
	"github.com/gabrielemastrapasqua/linklore/internal/llm"
	"github.com/gabrielemastrapasqua/linklore/internal/llm/fake"
	"github.com/gabrielemastrapasqua/linklore/internal/search"
	"github.com/gabrielemastrapasqua/linklore/internal/storage"
)

func newChatFixture(t *testing.T) (*Service, int64) {
	t.Helper()
	st, err := storage.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	col, _ := st.CreateCollection(context.Background(), "c", "C", "")
	l, _ := st.CreateLink(context.Background(), col.ID, "https://example.com/r")
	_ = st.UpdateLinkExtraction(context.Background(), l.ID,
		"Rust ownership", "Borrowing rules", "", "Rust uses ownership to manage memory.", "en", "")
	_ = st.UpdateLinkSummary(context.Background(), l.ID, "Primer on rust ownership.")
	ids, _ := st.InsertChunks(context.Background(), l.ID,
		[]string{"Rust ownership tracks resources at compile time."})
	// Embed the chunk so cosine has something to chew on.
	fb := &fake.Backend{EmbedDim: 8}
	res, _ := fb.Embed(context.Background(), []string{"Rust ownership tracks resources at compile time."}, nil)
	_ = st.SetChunkEmbedding(context.Background(), ids[0], embed.Encode(res.Vectors[0]))

	eng := search.New(st, fb)
	streamer := &fake.Backend{
		StreamChunks: []llm.StreamChunk{
			{Text: "Rust "}, {Text: "uses "}, {Text: "ownership."}, {Done: true},
		},
	}
	return New(st, eng, streamer), col.ID
}

func TestPrepare_persistsUserMsgAndCreatesSession(t *testing.T) {
	svc, colID := newChatFixture(t)
	turn, err := svc.Prepare(context.Background(), 0, colID, "what is rust ownership?")
	if err != nil {
		t.Fatal(err)
	}
	if turn.SessionID == 0 {
		t.Error("session not created")
	}
	if !strings.Contains(turn.Prompt, "what is rust ownership?") {
		t.Errorf("user message not in prompt: %s", turn.Prompt)
	}
	if !strings.Contains(turn.Prompt, "[src:") {
		t.Errorf("citation tag missing: %s", turn.Prompt)
	}
}

func TestPrepare_emptyMessageRejected(t *testing.T) {
	svc, colID := newChatFixture(t)
	if _, err := svc.Prepare(context.Background(), 0, colID, "  "); err == nil {
		t.Fatal("expected error")
	}
}

func TestStream_persistsAssistantAndForwardsChunks(t *testing.T) {
	svc, colID := newChatFixture(t)
	turn, err := svc.Prepare(context.Background(), 0, colID, "what is rust ownership?")
	if err != nil {
		t.Fatal(err)
	}

	var captured strings.Builder
	final, err := svc.Stream(context.Background(), turn.SessionID, turn.Prompt, func(t string) error {
		captured.WriteString(t)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if final == "" || final != captured.String() {
		t.Errorf("final %q vs captured %q", final, captured.String())
	}

	// Persisted: user + assistant messages.
	msgs, _ := svc.store.RecentChatMessages(context.Background(), turn.SessionID, 10)
	if len(msgs) != 2 {
		t.Fatalf("len = %d", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[1].Role != "assistant" {
		t.Errorf("roles: %v %v", msgs[0].Role, msgs[1].Role)
	}
	if !strings.Contains(msgs[1].Content, "ownership") {
		t.Errorf("assistant content lost: %q", msgs[1].Content)
	}
}

func TestStream_propagatesLLMError(t *testing.T) {
	svc, colID := newChatFixture(t)
	// Replace backend with one that errors on stream.
	svc.llm = &erroringStream{}
	turn, _ := svc.Prepare(context.Background(), 0, colID, "x")
	if _, err := svc.Stream(context.Background(), turn.SessionID, turn.Prompt, func(string) error { return nil }); err == nil {
		t.Fatal("expected error")
	}
}

func TestPrompt_includesHistoryWithoutCurrentDuplicated(t *testing.T) {
	svc, colID := newChatFixture(t)
	// First turn.
	t1, _ := svc.Prepare(context.Background(), 0, colID, "first question")
	_, _ = svc.Stream(context.Background(), t1.SessionID, t1.Prompt, func(string) error { return nil })
	// Second turn — same session.
	t2, err := svc.Prepare(context.Background(), t1.SessionID, colID, "follow up")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(t2.Prompt, "first question") {
		t.Errorf("history missing")
	}
	// "follow up" appears exactly once at the bottom of the prompt, not twice.
	if c := strings.Count(t2.Prompt, "follow up"); c != 1 {
		t.Errorf("current msg duplicated: count = %d", c)
	}
}

// ---- helpers ----

type erroringStream struct{}

func (erroringStream) Generate(context.Context, string, *llm.GenerateOptions) (*llm.GenerateResult, error) {
	return nil, errors.New("not used")
}
func (erroringStream) GenerateStream(context.Context, string, *llm.GenerateOptions) (<-chan llm.StreamChunk, error) {
	ch := make(chan llm.StreamChunk, 1)
	ch <- llm.StreamChunk{Error: errors.New("upstream failure")}
	close(ch)
	return ch, nil
}
func (erroringStream) Embed(context.Context, []string, *llm.EmbedOptions) (*llm.EmbedResult, error) {
	return nil, errors.New("not used")
}
