// Package chat builds RAG prompts and streams answers from the LLM backend.
//
// Flow per /chat/stream POST:
//  1. Persist the user's message (so a reload shows it).
//  2. Retrieve K chunks via search.Engine.RetrieveChunks (collection-scoped
//     when collectionID > 0; global otherwise).
//  3. Build the prompt: system + retrieved snippets (each tagged with
//     [src:<linkID>]) + recent history + current user message.
//  4. Stream tokens from llm.Backend.GenerateStream straight to the
//     ResponseWriter as SSE; persist the full assistant message at the end.
//
// Errors mid-stream are emitted as a final SSE "error" event so the UI can
// show them without breaking the stream framing.
package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gabrielemastrapasqua/linklore/internal/llm"
	"github.com/gabrielemastrapasqua/linklore/internal/search"
	"github.com/gabrielemastrapasqua/linklore/internal/storage"
)

// Service is the public type. It depends on storage (history persistence),
// search.Engine (retrieval), and an llm.Backend (streaming generation).
type Service struct {
	store  *storage.Store
	search *search.Engine
	llm    llm.Backend

	// TopK is how many chunks are pulled into context per turn. Default 8.
	TopK int
	// HistoryTurns is how many prior (user, assistant) pairs to include.
	// Default 6.
	HistoryTurns int
}

func New(store *storage.Store, eng *search.Engine, backend llm.Backend) *Service {
	return &Service{store: store, search: eng, llm: backend, TopK: 8, HistoryTurns: 6}
}

// Citation references one source chunk, exposed so the UI can render a
// "Sources" footer alongside the streamed answer.
type Citation struct {
	LinkID  int64
	Title   string
	URL     string
	Snippet string
}

// PrepareTurn does everything except the streaming call. Returned so that
// the HTTP handler can write citations into a SSE preamble before the model
// starts emitting tokens.
type PreparedTurn struct {
	SessionID int64
	Prompt    string
	Sources   []Citation
}

// Prepare retrieves context, persists the user message, and returns the
// composed prompt + citations. Does NOT call the LLM.
func (s *Service) Prepare(ctx context.Context, sessionID, collectionID int64, userMsg string) (*PreparedTurn, error) {
	userMsg = strings.TrimSpace(userMsg)
	if userMsg == "" {
		return nil, errors.New("empty user message")
	}

	// Lazy-create the session if the caller didn't have one.
	if sessionID == 0 {
		id, err := s.store.CreateChatSession(ctx, collectionID)
		if err != nil {
			return nil, fmt.Errorf("create session: %w", err)
		}
		sessionID = id
	}

	if _, err := s.store.AppendChatMessage(ctx, sessionID, "user", userMsg); err != nil {
		return nil, fmt.Errorf("append user msg: %w", err)
	}

	hits, err := s.search.RetrieveChunks(ctx, userMsg, collectionID, s.TopK, true)
	if err != nil {
		return nil, fmt.Errorf("retrieve chunks: %w", err)
	}

	history, err := s.store.RecentChatMessages(ctx, sessionID, s.HistoryTurns*2+1)
	if err != nil {
		return nil, fmt.Errorf("history: %w", err)
	}

	citations := make([]Citation, 0, len(hits))
	for _, h := range hits {
		title := h.Link.Title
		if title == "" {
			title = h.Link.URL
		}
		snip := truncate(h.Chunk.Text, 240)
		citations = append(citations, Citation{
			LinkID: h.Link.ID, Title: title, URL: h.Link.URL, Snippet: snip,
		})
	}

	return &PreparedTurn{
		SessionID: sessionID,
		Prompt:    buildPrompt(userMsg, citations, history),
		Sources:   citations,
	}, nil
}

// Stream runs the LLM and forwards every chunk to onChunk. The accumulated
// answer is persisted as the assistant message when the stream completes.
// Returns the final answer text so callers can include it in the SSE close
// event if they want.
func (s *Service) Stream(ctx context.Context, sessionID int64, prompt string, onChunk func(text string) error) (string, error) {
	ch, err := s.llm.GenerateStream(ctx, prompt, &llm.GenerateOptions{Temperature: 0.3})
	if err != nil {
		return "", fmt.Errorf("llm stream: %w", err)
	}
	var b strings.Builder
	for c := range ch {
		if c.Error != nil {
			return b.String(), c.Error
		}
		if c.Text != "" {
			b.WriteString(c.Text)
			if err := onChunk(c.Text); err != nil {
				return b.String(), err
			}
		}
		if c.Done {
			break
		}
	}
	if _, err := s.store.AppendChatMessage(ctx, sessionID, "assistant", b.String()); err != nil {
		return b.String(), fmt.Errorf("persist assistant msg: %w", err)
	}
	return b.String(), nil
}

// buildPrompt composes the system + sources + history + user-question prompt.
//
// Language: the assistant must reply in the SAME language as the user. We
// state this explicitly so qwen36-chat doesn't default to English when the
// user writes in Italian / French / etc. The system text is bilingual on
// purpose so the model lock-step matches whatever side it picks.
func buildPrompt(userMsg string, citations []Citation, history []storage.ChatMessage) string {
	var b strings.Builder
	b.WriteString("You are linklore, an assistant grounded ONLY on the user's saved links.\n")
	b.WriteString("Reply in the SAME language as the user's last message — if they write in Italian, reply in Italian; in French, in French; etc. Match the user's language exactly.\n")
	b.WriteString("Be concise. When you use a source, cite it inline like [src:<id>].\n")
	b.WriteString("If the sources don't answer the question, say so plainly in the user's language.\n\n")

	if len(citations) > 0 {
		b.WriteString("Sources / Fonti:\n")
		for _, c := range citations {
			fmt.Fprintf(&b, "[src:%d] %s\n%s\n\n", c.LinkID, c.Title, c.Snippet)
		}
	} else {
		b.WriteString("(no saved sources matched this question / nessuna fonte salvata corrisponde a questa domanda)\n\n")
	}

	if len(history) > 0 {
		b.WriteString("Conversation so far / Conversazione finora:\n")
		// history is oldest-first; skip the very last entry which IS the
		// current user message we just persisted.
		end := len(history)
		if end > 0 && history[end-1].Role == "user" && history[end-1].Content == userMsg {
			end--
		}
		for _, m := range history[:end] {
			fmt.Fprintf(&b, "%s: %s\n", m.Role, m.Content)
		}
		b.WriteByte('\n')
	}

	fmt.Fprintf(&b, "user: %s\nassistant:", userMsg)
	return b.String()
}

func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
