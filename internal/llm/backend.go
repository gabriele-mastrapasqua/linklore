// Package llm defines the Backend abstraction over local LLM endpoints
// (Ollama, LiteLLM+vLLM). The interface intentionally mirrors graphrag's so
// patterns and prompts stay portable. Linklore uses three operations:
//
//   - Generate         — one-shot text completion (e.g. summary JSON).
//   - GenerateStream   — token-by-token completion (RAG chat).
//   - Embed            — batch embeddings for chunks/queries.
package llm

import "context"

// GenerateOptions carries per-request knobs. All fields are optional;
// backends apply their own defaults when a value is zero.
type GenerateOptions struct {
	Model       string
	Temperature float64
	TopP        float64
	NumCtx      int
	Stop        []string
}

// GenerateResult is the return shape of a non-streaming Generate call.
type GenerateResult struct {
	Text   string
	Tokens int
}

// StreamChunk represents one slice of a streaming Generate response.
// A chunk with Done=true (and possibly empty Text) terminates the stream.
type StreamChunk struct {
	Text  string
	Done  bool
	Error error
}

// EmbedOptions controls batch behaviour and model selection.
type EmbedOptions struct {
	Model     string
	BatchSize int
}

// EmbedResult is parallel to the input []string; vectors[i] embeds texts[i].
type EmbedResult struct {
	Vectors [][]float32
}

// Backend is implemented by every LLM provider plus the test fake.
type Backend interface {
	Generate(ctx context.Context, prompt string, opts *GenerateOptions) (*GenerateResult, error)
	GenerateStream(ctx context.Context, prompt string, opts *GenerateOptions) (<-chan StreamChunk, error)
	Embed(ctx context.Context, texts []string, opts *EmbedOptions) (*EmbedResult, error)
}

// HealthChecker is an optional capability: backends that implement it can
// be probed with a cheap "are you alive?" call. The worker uses this to
// decide whether to attempt summary/embed for a given tick — when the
// gateway is down we'd rather skip than rack up retries that all fail
// the same way. Backends that don't implement it are assumed healthy.
type HealthChecker interface {
	Healthcheck(ctx context.Context) error
}

// Backend identifiers used in config + UI hints. Always reference these
// constants instead of bare string literals so a typo at the call site
// becomes a compile error.
const (
	BackendNone    = "none"
	BackendOllama  = "ollama"
	BackendLitellm = "litellm"
)
