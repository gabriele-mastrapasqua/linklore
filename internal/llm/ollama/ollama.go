// Package ollama implements llm.Backend against an Ollama server. It speaks
// the /api/generate (NDJSON streaming) and /api/embed endpoints — the same
// pattern graphrag/internal/llm/ollama uses.
package ollama

import (
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
	Host       string
	Model      string
	EmbedModel string
	NumCtx     int
	Timeout    time.Duration
}

type Backend struct {
	cfg    Config
	client *http.Client
}

func New(cfg Config) (*Backend, error) {
	if cfg.Host == "" {
		return nil, errors.New("ollama: host required")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 120 * time.Second
	}
	cfg.Host = strings.TrimRight(cfg.Host, "/")
	return &Backend{cfg: cfg, client: &http.Client{Timeout: cfg.Timeout}}, nil
}

// Wire types follow Ollama's REST shape.
type generateReq struct {
	Model   string         `json:"model"`
	Prompt  string         `json:"prompt"`
	Stream  bool           `json:"stream"`
	Options map[string]any `json:"options,omitempty"`
}

type generateResp struct {
	Response string `json:"response"`
	Done     bool   `json:"done"`
	Eval     int    `json:"eval_count"`
	Error    string `json:"error,omitempty"`
}

type embedReq struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embedResp struct {
	Embeddings [][]float32 `json:"embeddings"`
	Error      string      `json:"error,omitempty"`
}

func (b *Backend) buildOptions(o *llm.GenerateOptions) map[string]any {
	out := map[string]any{}
	if b.cfg.NumCtx > 0 {
		out["num_ctx"] = b.cfg.NumCtx
	}
	if o == nil {
		return out
	}
	if o.NumCtx > 0 {
		out["num_ctx"] = o.NumCtx
	}
	if o.Temperature > 0 {
		out["temperature"] = o.Temperature
	}
	if o.TopP > 0 {
		out["top_p"] = o.TopP
	}
	if len(o.Stop) > 0 {
		out["stop"] = o.Stop
	}
	return out
}

func (b *Backend) modelFor(o *llm.GenerateOptions) string {
	if o != nil && o.Model != "" {
		return o.Model
	}
	return b.cfg.Model
}

func (b *Backend) Generate(ctx context.Context, prompt string, opts *llm.GenerateOptions) (*llm.GenerateResult, error) {
	body, err := json.Marshal(generateReq{
		Model: b.modelFor(opts), Prompt: prompt, Stream: false, Options: b.buildOptions(opts),
	})
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.cfg.Host+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama generate: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ollama generate: status %d: %s", resp.StatusCode, raw)
	}
	var gr generateResp
	if err := json.NewDecoder(resp.Body).Decode(&gr); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if gr.Error != "" {
		return nil, errors.New("ollama: " + gr.Error)
	}
	return &llm.GenerateResult{Text: gr.Response, Tokens: gr.Eval}, nil
}

func (b *Backend) GenerateStream(ctx context.Context, prompt string, opts *llm.GenerateOptions) (<-chan llm.StreamChunk, error) {
	body, err := json.Marshal(generateReq{
		Model: b.modelFor(opts), Prompt: prompt, Stream: true, Options: b.buildOptions(opts),
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.cfg.Host+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama stream: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("ollama stream: status %d: %s", resp.StatusCode, raw)
	}

	out := make(chan llm.StreamChunk, 16)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		dec := json.NewDecoder(resp.Body)
		for {
			var gr generateResp
			if err := dec.Decode(&gr); err != nil {
				if errors.Is(err, io.EOF) {
					return
				}
				select {
				case out <- llm.StreamChunk{Error: fmt.Errorf("decode: %w", err)}:
				case <-ctx.Done():
				}
				return
			}
			if gr.Error != "" {
				out <- llm.StreamChunk{Error: errors.New(gr.Error)}
				return
			}
			out <- llm.StreamChunk{Text: gr.Response, Done: gr.Done}
			if gr.Done {
				return
			}
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
		return nil, errors.New("ollama: embed_model required")
	}

	all := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += batch {
		end := min(start+batch, len(texts))
		body, err := json.Marshal(embedReq{Model: model, Input: texts[start:end]})
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.cfg.Host+"/api/embed", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := b.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("ollama embed: %w", err)
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("ollama embed: status %d: %s", resp.StatusCode, raw)
		}
		var er embedResp
		if err := json.Unmarshal(raw, &er); err != nil {
			return nil, fmt.Errorf("decode embed: %w", err)
		}
		if er.Error != "" {
			return nil, errors.New("ollama embed: " + er.Error)
		}
		if len(er.Embeddings) != end-start {
			return nil, fmt.Errorf("ollama embed: expected %d vectors, got %d", end-start, len(er.Embeddings))
		}
		all = append(all, er.Embeddings...)
	}
	return &llm.EmbedResult{Vectors: all}, nil
}
