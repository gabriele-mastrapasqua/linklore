// Package fake provides a llm.Backend test double. It returns deterministic
// canned responses (or whatever the test sets via the public fields) without
// making any network calls — every package that depends on llm uses this in
// its own tests.
package fake

import (
	"context"
	"hash/fnv"
	"strings"
	"sync/atomic"

	"github.com/gabriele-mastrapasqua/linklore/internal/llm"
)

type Backend struct {
	// GenerateText overrides the canned response for Generate when non-empty.
	GenerateText string
	// GenerateError forces Generate / GenerateStream to return this error.
	GenerateError error
	// EmbedDim is the length of every fake embedding vector (default 8).
	EmbedDim int
	// StreamChunks, when non-empty, is replayed by GenerateStream as-is.
	// Otherwise the fake splits GenerateText into runes and emits one per chunk.
	StreamChunks []llm.StreamChunk

	calls atomic.Int64
}

func (b *Backend) Calls() int64 { return b.calls.Load() }

func (b *Backend) Generate(_ context.Context, prompt string, _ *llm.GenerateOptions) (*llm.GenerateResult, error) {
	b.calls.Add(1)
	if b.GenerateError != nil {
		return nil, b.GenerateError
	}
	if b.GenerateText != "" {
		return &llm.GenerateResult{Text: b.GenerateText, Tokens: len(b.GenerateText) / 4}, nil
	}
	// Echo prompt for tests that don't care about the body.
	return &llm.GenerateResult{Text: "echo:" + prompt, Tokens: len(prompt) / 4}, nil
}

func (b *Backend) GenerateStream(ctx context.Context, prompt string, _ *llm.GenerateOptions) (<-chan llm.StreamChunk, error) {
	b.calls.Add(1)
	if b.GenerateError != nil {
		return nil, b.GenerateError
	}
	out := make(chan llm.StreamChunk, 8)
	go func() {
		defer close(out)
		if len(b.StreamChunks) > 0 {
			for _, c := range b.StreamChunks {
				select {
				case out <- c:
				case <-ctx.Done():
					return
				}
			}
			return
		}
		text := b.GenerateText
		if text == "" {
			text = "echo:" + prompt
		}
		// Emit token-ish chunks word-by-word so consumers see > 1 chunk.
		// strings.Fields drops repeated whitespace; we re-add a trailing space
		// on every word except the last so consumers can simply concatenate.
		words := strings.Fields(text)
		for i, w := range words {
			piece := w
			if i < len(words)-1 {
				piece += " "
			}
			select {
			case out <- llm.StreamChunk{Text: piece}:
			case <-ctx.Done():
				return
			}
		}
		select {
		case out <- llm.StreamChunk{Done: true}:
		case <-ctx.Done():
		}
	}()
	return out, nil
}

func (b *Backend) Embed(_ context.Context, texts []string, _ *llm.EmbedOptions) (*llm.EmbedResult, error) {
	b.calls.Add(1)
	dim := b.EmbedDim
	if dim <= 0 {
		dim = 8
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		// Deterministic vector: hash text into the first slot, fill rest with
		// values that depend on hash + position, then L2-normalise.
		h := fnv.New64a()
		h.Write([]byte(t))
		seed := h.Sum64()
		v := make([]float32, dim)
		var sumsq float32
		for j := range dim {
			x := float32((seed>>(j%64))&0xFF) / 255.0
			v[j] = x
			sumsq += x * x
		}
		if sumsq > 0 {
			inv := 1.0 / float32Sqrt(sumsq)
			for j := range v {
				v[j] *= inv
			}
		}
		out[i] = v
	}
	return &llm.EmbedResult{Vectors: out}, nil
}

// float32Sqrt avoids importing math just for the sqrt call.
func float32Sqrt(x float32) float32 {
	if x == 0 {
		return 0
	}
	z := x
	// Newton-Raphson, plenty accurate for our use.
	for range 5 {
		z = (z + x/z) / 2
	}
	return z
}
