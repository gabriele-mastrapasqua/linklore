package chat

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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
	final, stats, err := svc.Stream(context.Background(), turn.SessionID, turn.Prompt, StreamCallbacks{
		OnChunk: func(t string, _ StreamStats) error {
			captured.WriteString(t)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if final == "" || final != captured.String() {
		t.Errorf("final %q vs captured %q", final, captured.String())
	}
	if stats.Tokens == 0 {
		t.Errorf("expected tokens > 0, got %+v", stats)
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
	if _, _, err := svc.Stream(context.Background(), turn.SessionID, turn.Prompt, StreamCallbacks{}); err == nil {
		t.Fatal("expected error")
	}
}

// End-to-end: Prepare → Stream → Persist, with the chunk we seeded as
// the only candidate. Asserts that the model's tokens reach the caller in
// order, that citations include the seeded link, and that the assistant
// reply lands in chat_messages.
func TestE2E_PrepareStreamPersist(t *testing.T) {
	svc, colID := newChatFixture(t)

	turn, err := svc.Prepare(context.Background(), 0, colID, "what is rust ownership")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if len(turn.Sources) == 0 {
		t.Fatalf("expected at least one cited source")
	}
	gotID := turn.Sources[0].LinkID
	if gotID == 0 {
		t.Errorf("source has no link id: %+v", turn.Sources[0])
	}

	var captured []string
	final, _, err := svc.Stream(context.Background(), turn.SessionID, turn.Prompt,
		StreamCallbacks{OnChunk: func(t string, _ StreamStats) error {
			captured = append(captured, t)
			return nil
		}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if len(captured) < 2 {
		t.Errorf("expected multiple streamed chunks, got %d", len(captured))
	}
	if final != strings.Join(captured, "") {
		t.Errorf("final = %q, captured = %q", final, strings.Join(captured, ""))
	}

	msgs, _ := svc.store.RecentChatMessages(context.Background(), turn.SessionID, 10)
	if len(msgs) != 2 {
		t.Fatalf("messages = %d, want 2 (user + assistant)", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "what is rust ownership" {
		t.Errorf("user msg: %+v", msgs[0])
	}
	if msgs[1].Role != "assistant" {
		t.Errorf("assistant msg role: %v", msgs[1].Role)
	}
}

// E2E across two turns to verify history reuse + incremental persistence.
func TestE2E_TwoTurnConversation(t *testing.T) {
	svc, colID := newChatFixture(t)

	t1, _ := svc.Prepare(context.Background(), 0, colID, "first question")
	if _, _, err := svc.Stream(context.Background(), t1.SessionID, t1.Prompt, StreamCallbacks{}); err != nil {
		t.Fatal(err)
	}
	t2, err := svc.Prepare(context.Background(), t1.SessionID, colID, "follow up")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(t2.Prompt, "first question") {
		t.Errorf("history missing in turn 2 prompt")
	}
	if _, _, err := svc.Stream(context.Background(), t2.SessionID, t2.Prompt, StreamCallbacks{}); err != nil {
		t.Fatal(err)
	}
	msgs, _ := svc.store.RecentChatMessages(context.Background(), t1.SessionID, 10)
	if len(msgs) != 4 {
		t.Errorf("expected 4 persisted messages across 2 turns, got %d", len(msgs))
	}
}

func TestStream_TPS_isMeasuredAndMonotonic(t *testing.T) {
	svc, colID := newChatFixture(t)
	turn, err := svc.Prepare(context.Background(), 0, colID, "rust ownership")
	if err != nil {
		t.Fatal(err)
	}

	var perChunk []StreamStats
	_, finalStats, err := svc.Stream(context.Background(), turn.SessionID, turn.Prompt,
		StreamCallbacks{OnChunk: func(_ string, s StreamStats) error {
			perChunk = append(perChunk, s)
			return nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	if len(perChunk) < 2 {
		t.Fatalf("expected ≥2 chunks, got %d", len(perChunk))
	}

	// Token count must be monotonically non-decreasing across callbacks.
	for i := 1; i < len(perChunk); i++ {
		if perChunk[i].Tokens < perChunk[i-1].Tokens {
			t.Errorf("tokens went backwards at i=%d: %+v", i, perChunk)
		}
	}
	if finalStats.Tokens < perChunk[len(perChunk)-1].Tokens {
		t.Errorf("final tokens < last per-chunk: %d vs %d",
			finalStats.Tokens, perChunk[len(perChunk)-1].Tokens)
	}
}

func TestStreamStats_TPSEdgeCases(t *testing.T) {
	if got := (StreamStats{Tokens: 5, Duration: 0}).TPS(); got != 0 {
		t.Errorf("zero duration TPS = %v, want 0", got)
	}
	got := (StreamStats{Tokens: 100, Duration: 2 * time.Second}).TPS()
	if got < 49 || got > 51 {
		t.Errorf("100 tok / 2s = %v, want ~50", got)
	}
}

func TestPrompt_includesHistoryWithoutCurrentDuplicated(t *testing.T) {
	svc, colID := newChatFixture(t)
	// First turn.
	t1, _ := svc.Prepare(context.Background(), 0, colID, "first question")
	_, _, _ = svc.Stream(context.Background(), t1.SessionID, t1.Prompt, StreamCallbacks{})
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
