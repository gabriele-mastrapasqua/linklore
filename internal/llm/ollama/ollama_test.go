package ollama

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gabriele-mastrapasqua/linklore/internal/llm"
)

func newServer(h http.HandlerFunc) (*Backend, *httptest.Server) {
	ts := httptest.NewServer(h)
	b, _ := New(Config{Host: ts.URL, Model: "qwen", EmbedModel: "nomic", Timeout: 2 * time.Second})
	return b, ts
}

func TestNew_validation(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Error("expected error on missing host")
	}
}

func TestGenerate_OK(t *testing.T) {
	b, ts := newServer(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			t.Errorf("path = %s", r.URL.Path)
		}
		var req map[string]any
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		if req["model"] != "qwen" {
			t.Errorf("model = %v", req["model"])
		}
		if req["stream"] != false {
			t.Errorf("expected stream=false")
		}
		w.Write([]byte(`{"response":"hello world","done":true,"eval_count":12}`))
	})
	defer ts.Close()

	r, err := b.Generate(context.Background(), "say hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Text != "hello world" || r.Tokens != 12 {
		t.Errorf("got %+v", r)
	}
}

func TestGenerate_HTTPError(t *testing.T) {
	b, ts := newServer(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	defer ts.Close()
	if _, err := b.Generate(context.Background(), "x", nil); err == nil {
		t.Error("expected error on 500")
	}
}

func TestGenerate_ModelOverride(t *testing.T) {
	b, ts := newServer(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"model":"override"`) {
			t.Errorf("model not overridden: %s", body)
		}
		w.Write([]byte(`{"response":"ok","done":true}`))
	})
	defer ts.Close()
	if _, err := b.Generate(context.Background(), "x",
		&llm.GenerateOptions{Model: "override"}); err != nil {
		t.Fatal(err)
	}
}

func TestStream_NDJSON(t *testing.T) {
	b, ts := newServer(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Write([]byte(`{"response":"hel","done":false}` + "\n"))
		w.Write([]byte(`{"response":"lo","done":false}` + "\n"))
		w.Write([]byte(`{"response":"","done":true,"eval_count":5}` + "\n"))
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
			t.Fatalf("stream err: %v", c.Error)
		}
		got = append(got, c.Text)
		if c.Done {
			sawDone = true
		}
	}
	if strings.Join(got, "") != "hello" {
		t.Errorf("joined = %q", strings.Join(got, ""))
	}
	if !sawDone {
		t.Error("never saw Done")
	}
}

func TestEmbed_BatchAndOrder(t *testing.T) {
	calls := 0
	b, ts := newServer(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req embedReq
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		// echo a vector per input
		vecs := make([][]float32, len(req.Input))
		for i := range req.Input {
			vecs[i] = []float32{float32(i), float32(len(req.Input[i]))}
		}
		json.NewEncoder(w).Encode(embedResp{Embeddings: vecs})
	})
	defer ts.Close()

	texts := []string{"a", "bb", "ccc", "dddd", "eeeee"}
	r, err := b.Embed(context.Background(), texts,
		&llm.EmbedOptions{Model: "nomic", BatchSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Errorf("expected 3 batch calls, got %d", calls)
	}
	if len(r.Vectors) != len(texts) {
		t.Fatalf("len = %d", len(r.Vectors))
	}
	if r.Vectors[2][1] != 3 {
		t.Errorf("ccc len-vector wrong: %v", r.Vectors[2])
	}
}

func TestEmbed_emptyInput(t *testing.T) {
	b, _ := New(Config{Host: "http://x", EmbedModel: "n"})
	r, err := b.Embed(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Vectors) != 0 {
		t.Errorf("expected no vectors")
	}
}

func TestEmbed_missingEmbedModel(t *testing.T) {
	b, _ := New(Config{Host: "http://x"})
	if _, err := b.Embed(context.Background(), []string{"x"}, nil); err == nil {
		t.Error("expected error for missing embed model")
	}
}
