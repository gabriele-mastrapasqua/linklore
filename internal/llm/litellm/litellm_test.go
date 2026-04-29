package litellm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newServer(h http.HandlerFunc) (*Backend, *httptest.Server) {
	ts := httptest.NewServer(h)
	b, _ := New(Config{
		BaseURL: ts.URL, Model: "qwen-vllm", EmbedModel: "nomic",
		APIKey: "secret", Timeout: 2 * time.Second,
	})
	return b, ts
}

func TestNew_validation(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Error("expected error for missing base_url")
	}
}

func TestChat_modelDefault_isConfigModel(t *testing.T) {
	// When GenerateOptions.Model is empty, the request must carry the
	// model from the Backend config (e.g. "qwen3:14b") — that's what
	// drives the user toward the fast vLLM model on the gateway.
	b, ts := newServer(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"model":"qwen-vllm"`) {
			t.Errorf("expected default model in payload: %s", body)
		}
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	})
	defer ts.Close()
	if _, err := b.Generate(context.Background(), "x", nil); err != nil {
		t.Fatal(err)
	}
}

func TestChat_OK_authHeader(t *testing.T) {
	b, ts := newServer(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("auth header = %q", r.Header.Get("Authorization"))
		}
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi there"}}],"usage":{"completion_tokens":3}}`))
	})
	defer ts.Close()

	r, err := b.Generate(context.Background(), "say hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Text != "hi there" || r.Tokens != 3 {
		t.Errorf("got %+v", r)
	}
}

func TestChat_HTTPError(t *testing.T) {
	b, ts := newServer(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	})
	defer ts.Close()
	if _, err := b.Generate(context.Background(), "x", nil); err == nil {
		t.Error("expected error")
	}
}

func TestStream_SSE(t *testing.T) {
	b, ts := newServer(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// First chunk
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hel\"}}]}\n\n"))
		// Second chunk
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n"))
		// Termination
		w.Write([]byte("data: [DONE]\n\n"))
	})
	defer ts.Close()

	ch, err := b.GenerateStream(context.Background(), "x", nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	var sawDone bool
	for c := range ch {
		if c.Error != nil {
			t.Fatalf("err: %v", c.Error)
		}
		if c.Done {
			sawDone = true
		}
		if c.Text != "" {
			got = append(got, c.Text)
		}
	}
	if strings.Join(got, "") != "hello" {
		t.Errorf("joined = %q", strings.Join(got, ""))
	}
	if !sawDone {
		t.Error("expected DONE")
	}
}

func TestEmbed_OK_indicesRespected(t *testing.T) {
	b, ts := newServer(func(w http.ResponseWriter, r *http.Request) {
		var req embedReq
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		// Return items in REVERSE order with explicit indices to verify sort.
		items := make([]embedRespItem, len(req.Input))
		for i := range req.Input {
			items[len(req.Input)-1-i] = embedRespItem{
				Index:     i,
				Embedding: []float32{float32(i)},
			}
		}
		json.NewEncoder(w).Encode(embedResp{Data: items})
	})
	defer ts.Close()

	r, err := b.Embed(context.Background(), []string{"a", "b", "c"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range r.Vectors {
		if v[0] != float32(i) {
			t.Errorf("vector[%d] not at right slot: %v", i, v)
		}
	}
}

func TestEmbed_emptyInput(t *testing.T) {
	b, _ := New(Config{BaseURL: "http://x", EmbedModel: "n"})
	r, err := b.Embed(context.Background(), nil, nil)
	if err != nil || len(r.Vectors) != 0 {
		t.Errorf("expected empty: %v %v", r, err)
	}
}

func TestEmbed_missingModel(t *testing.T) {
	b, _ := New(Config{BaseURL: "http://x"})
	if _, err := b.Embed(context.Background(), []string{"x"}, nil); err == nil {
		t.Error("expected error")
	}
}
