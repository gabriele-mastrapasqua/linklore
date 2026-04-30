package fake

import (
	"context"
	"errors"
	"testing"

	"github.com/gabriele-mastrapasqua/linklore/internal/llm"
)

func TestGenerate_default(t *testing.T) {
	b := &Backend{}
	r, err := b.Generate(context.Background(), "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Text != "echo:hi" {
		t.Errorf("text = %q", r.Text)
	}
}

func TestGenerate_canned(t *testing.T) {
	b := &Backend{GenerateText: "{\"tldr\":\"x\",\"tags\":[\"y\"]}"}
	r, err := b.Generate(context.Background(), "anything", nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Text != b.GenerateText {
		t.Errorf("override ignored")
	}
}

func TestGenerate_error(t *testing.T) {
	want := errors.New("boom")
	b := &Backend{GenerateError: want}
	if _, err := b.Generate(context.Background(), "x", nil); !errors.Is(err, want) {
		t.Errorf("err = %v", err)
	}
}

func TestStream_default_emitsMultipleChunksAndDone(t *testing.T) {
	b := &Backend{}
	ch, err := b.GenerateStream(context.Background(), "hello world", nil)
	if err != nil {
		t.Fatal(err)
	}
	chunks := drainChunks(ch)
	if len(chunks) < 2 {
		t.Errorf("expected multiple chunks, got %d", len(chunks))
	}
	if !chunks[len(chunks)-1].Done {
		t.Errorf("last chunk not Done")
	}
}

func TestStream_canned(t *testing.T) {
	b := &Backend{StreamChunks: []llm.StreamChunk{
		{Text: "a"}, {Text: "b"}, {Done: true},
	}}
	ch, _ := b.GenerateStream(context.Background(), "x", nil)
	chunks := drainChunks(ch)
	if len(chunks) != 3 || chunks[0].Text != "a" || !chunks[2].Done {
		t.Errorf("got %+v", chunks)
	}
}

func TestEmbed_dimAndDeterminism(t *testing.T) {
	b := &Backend{EmbedDim: 16}
	r1, _ := b.Embed(context.Background(), []string{"alpha", "beta"}, nil)
	r2, _ := b.Embed(context.Background(), []string{"alpha", "beta"}, nil)
	if len(r1.Vectors) != 2 || len(r1.Vectors[0]) != 16 {
		t.Fatalf("shape: %v", r1.Vectors)
	}
	for i := range 16 {
		if r1.Vectors[0][i] != r2.Vectors[0][i] {
			t.Errorf("non-deterministic at %d", i)
		}
	}
	// Different inputs → different vectors.
	if r1.Vectors[0][0] == r1.Vectors[1][0] && r1.Vectors[0][7] == r1.Vectors[1][7] {
		t.Errorf("vectors should differ across different inputs")
	}
}

func TestCallsCounter(t *testing.T) {
	b := &Backend{}
	b.Generate(context.Background(), "x", nil)
	b.Embed(context.Background(), []string{"a"}, nil)
	if b.Calls() != 2 {
		t.Errorf("calls = %d", b.Calls())
	}
}

func drainChunks(ch <-chan llm.StreamChunk) []llm.StreamChunk {
	var out []llm.StreamChunk
	for c := range ch {
		out = append(out, c)
	}
	return out
}
