// Package litellm implements llm.Backend against an OpenAI-compatible
// proxy (LiteLLM in front of vLLM/etc). Only the subset linklore actually
// uses is wired: /chat/completions (with stream=true SSE) and /embeddings.
package litellm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gabrielemastrapasqua/linklore/internal/llm"
)

type Config struct {
	BaseURL    string
	Model      string
	EmbedModel string
	APIKey     string
	Timeout    time.Duration
}

type Backend struct {
	cfg    Config
	client *http.Client
}

func New(cfg Config) (*Backend, error) {
	if cfg.BaseURL == "" {
		return nil, errors.New("litellm: base_url required")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 120 * time.Second
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	return &Backend{cfg: cfg, client: &http.Client{Timeout: cfg.Timeout}}, nil
}

// OpenAI chat-completion wire types — only the fields we read.
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type chatReq struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Stream      bool          `json:"stream"`
	Temperature float64       `json:"temperature,omitempty"`
	TopP        float64       `json:"top_p,omitempty"`
	Stop        []string      `json:"stop,omitempty"`
}
type chatChoice struct {
	Message chatMessage `json:"message"`
	Delta   chatMessage `json:"delta"`
}
type chatResp struct {
	Choices []chatChoice `json:"choices"`
	Usage   struct {
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

type embedReq struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}
type embedRespItem struct {
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}
type embedResp struct {
	Data []embedRespItem `json:"data"`
}

// Healthcheck does a HEAD/GET on /v1/models. Cheap (returns metadata
// only), and the master key already has access to it, so this is what
// graphrag uses too. A non-2xx response or any transport error counts
// as "down".
func (b *Backend) Healthcheck(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.cfg.BaseURL+"/models", nil)
	if err != nil {
		return err
	}
	if b.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+b.cfg.APIKey)
	}
	// Independent client with a short timeout — health probes shouldn't
	// hang on a partial outage.
	hc := &http.Client{Timeout: 5 * time.Second}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("litellm health: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("litellm health: status %d: %s", resp.StatusCode, raw)
	}
	return nil
}

func (b *Backend) modelFor(o *llm.GenerateOptions) string {
	if o != nil && o.Model != "" {
		return o.Model
	}
	return b.cfg.Model
}

func (b *Backend) newRequest(ctx context.Context, path string, body []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.cfg.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if b.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+b.cfg.APIKey)
	}
	return req, nil
}

func (b *Backend) Generate(ctx context.Context, prompt string, opts *llm.GenerateOptions) (*llm.GenerateResult, error) {
	body, err := json.Marshal(buildChatReq(b.modelFor(opts), prompt, false, opts))
	if err != nil {
		return nil, err
	}
	req, err := b.newRequest(ctx, "/chat/completions", body)
	if err != nil {
		return nil, err
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("litellm chat: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("litellm chat: status %d: %s", resp.StatusCode, raw)
	}
	var cr chatResp
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return nil, err
	}
	if len(cr.Choices) == 0 {
		return nil, errors.New("litellm: no choices in response")
	}
	return &llm.GenerateResult{Text: cr.Choices[0].Message.Content, Tokens: cr.Usage.CompletionTokens}, nil
}

func (b *Backend) GenerateStream(ctx context.Context, prompt string, opts *llm.GenerateOptions) (<-chan llm.StreamChunk, error) {
	body, err := json.Marshal(buildChatReq(b.modelFor(opts), prompt, true, opts))
	if err != nil {
		return nil, err
	}
	req, err := b.newRequest(ctx, "/chat/completions", body)
	if err != nil {
		return nil, err
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("litellm stream: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("litellm stream: status %d: %s", resp.StatusCode, raw)
	}

	out := make(chan llm.StreamChunk, 16)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		// OpenAI SSE: lines beginning with "data: " carry JSON; "data: [DONE]" terminates.
		sc := bufio.NewScanner(resp.Body)
		// Default scanner buffer is 64KB; chunks can be larger.
		sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for sc.Scan() {
			line := sc.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			payload := strings.TrimPrefix(line, "data: ")
			if payload == "[DONE]" {
				out <- llm.StreamChunk{Done: true}
				return
			}
			var cr chatResp
			if err := json.Unmarshal([]byte(payload), &cr); err != nil {
				out <- llm.StreamChunk{Error: fmt.Errorf("decode sse: %w", err)}
				return
			}
			if len(cr.Choices) == 0 {
				continue
			}
			out <- llm.StreamChunk{Text: cr.Choices[0].Delta.Content}
		}
		if err := sc.Err(); err != nil {
			out <- llm.StreamChunk{Error: err}
		}
	}()
	return out, nil
}

func (b *Backend) Embed(ctx context.Context, texts []string, opts *llm.EmbedOptions) (*llm.EmbedResult, error) {
	if len(texts) == 0 {
		return &llm.EmbedResult{}, nil
	}
	model := b.cfg.EmbedModel
	batch := 32
	if opts != nil {
		if opts.Model != "" {
			model = opts.Model
		}
		if opts.BatchSize > 0 {
			batch = opts.BatchSize
		}
	}
	if model == "" {
		return nil, errors.New("litellm: embed_model required")
	}

	all := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += batch {
		end := min(start+batch, len(texts))
		body, err := json.Marshal(embedReq{Model: model, Input: texts[start:end]})
		if err != nil {
			return nil, err
		}
		req, err := b.newRequest(ctx, "/embeddings", body)
		if err != nil {
			return nil, err
		}
		resp, err := b.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("litellm embed: %w", err)
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("litellm embed: status %d: %s", resp.StatusCode, raw)
		}
		var er embedResp
		if err := json.Unmarshal(raw, &er); err != nil {
			return nil, fmt.Errorf("decode embed: %w", err)
		}
		if len(er.Data) != end-start {
			return nil, fmt.Errorf("litellm embed: expected %d vectors, got %d", end-start, len(er.Data))
		}
		// data items are unordered in theory; sort by Index defensively.
		batchOut := make([][]float32, end-start)
		for _, it := range er.Data {
			if it.Index < 0 || it.Index >= len(batchOut) {
				return nil, fmt.Errorf("litellm embed: bad index %d", it.Index)
			}
			batchOut[it.Index] = it.Embedding
		}
		all = append(all, batchOut...)
	}
	return &llm.EmbedResult{Vectors: all}, nil
}

func buildChatReq(model, prompt string, stream bool, opts *llm.GenerateOptions) chatReq {
	r := chatReq{
		Model:    model,
		Stream:   stream,
		Messages: []chatMessage{{Role: "user", Content: prompt}},
	}
	if opts != nil {
		r.Temperature = opts.Temperature
		r.TopP = opts.TopP
		r.Stop = opts.Stop
	}
	return r
}
