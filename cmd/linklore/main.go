package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/gabrielemastrapasqua/linklore/internal/config"
	"github.com/gabrielemastrapasqua/linklore/internal/extract"
	"github.com/gabrielemastrapasqua/linklore/internal/llm"
	"github.com/gabrielemastrapasqua/linklore/internal/llm/litellm"
	"github.com/gabrielemastrapasqua/linklore/internal/llm/ollama"
	"github.com/gabrielemastrapasqua/linklore/internal/search"
	"github.com/gabrielemastrapasqua/linklore/internal/server"
	"github.com/gabrielemastrapasqua/linklore/internal/storage"
	"github.com/gabrielemastrapasqua/linklore/internal/worker"
)

const usage = `linklore - local-first link manager

Usage:
  linklore serve   [--config PATH]
  linklore add     URL [-c SLUG] [--config PATH]
  linklore reindex [--config PATH]

Run "linklore <subcommand> -h" for flags.`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "serve":
		runServe(os.Args[2:])
	case "add":
		runAdd(os.Args[2:])
	case "reindex":
		runReindex(os.Args[2:])
	case "-h", "--help", "help":
		fmt.Println(usage)
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
}

func loadConfig(path string) config.Config {
	if path == "" {
		// Default to ./configs/config.yaml if present, else fall back to defaults+env.
		if _, err := os.Stat("./configs/config.yaml"); err == nil {
			path = "./configs/config.yaml"
		}
	}
	cfg, err := config.Load(path)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	return cfg
}

func openStore(ctx context.Context, dbPath string) *storage.Store {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		log.Fatalf("mkdir data: %v", err)
	}
	s, err := storage.Open(ctx, dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	return s
}

func runServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	cfgPath := fs.String("config", "", "path to config.yaml")
	_ = fs.Parse(args)

	cfg := loadConfig(*cfgPath)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	store := openStore(ctx, cfg.Database.Path)
	defer func() { _ = store.Close() }()

	backend, backendErr := newLLMBackend(cfg)
	if backendErr != nil {
		// Non-fatal: server still works for CRUD; worker idle until backend reachable.
		log.Printf("llm backend disabled: %v — UI runs in BM25-only / no-summary mode", backendErr)
	}
	var eng *search.Engine
	if backend != nil {
		eng = search.New(store, backend)
	} else {
		eng = search.New(store, nil)
	}

	if backend != nil {
		w := worker.New(store, backend, extract.NewFetcher(time.Duration(cfg.Worker.FetchTimeoutSeconds)*time.Second), cfg, worker.Options{})
		go func() {
			if err := w.Run(ctx); err != nil && err != context.Canceled {
				log.Printf("worker exited: %v", err)
			}
		}()
	}

	srv, err := server.New(cfg, store, eng)
	if err != nil {
		log.Fatalf("server: %v", err)
	}
	httpSrv := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()
	log.Printf("linklore: listening on %s (db=%s, llm.backend=%s)",
		cfg.Server.Addr, cfg.Database.Path, cfg.LLM.Backend)
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server: %v", err)
	}
}

func runAdd(args []string) {
	fs := flag.NewFlagSet("add", flag.ExitOnError)
	slug := fs.String("c", "default", "collection slug")
	cfgPath := fs.String("config", "", "path to config.yaml")
	_ = fs.Parse(args)
	rest := fs.Args()
	if len(rest) != 1 {
		log.Fatal("usage: linklore add URL [-c SLUG]")
	}
	cfg := loadConfig(*cfgPath)
	ctx := context.Background()
	store := openStore(ctx, cfg.Database.Path)
	defer func() { _ = store.Close() }()

	col, err := store.GetCollectionBySlug(ctx, *slug)
	if err == storage.ErrNotFound {
		col, err = store.CreateCollection(ctx, *slug, *slug, "")
	}
	if err != nil {
		log.Fatalf("collection: %v", err)
	}
	link, err := store.CreateLink(ctx, col.ID, rest[0])
	if err != nil {
		log.Fatalf("create link: %v", err)
	}
	fmt.Printf("queued link id=%d url=%s collection=%s\n", link.ID, link.URL, col.Slug)
}

// newLLMBackend constructs an llm.Backend from config. Returns (nil, nil)
// when the backend is intentionally disabled — currently we always try, but
// the caller treats a non-nil error as "degraded mode".
func newLLMBackend(cfg config.Config) (llm.Backend, error) {
	switch cfg.LLM.Backend {
	case "ollama":
		return ollama.New(ollama.Config{
			Host:       cfg.LLM.Ollama.Host,
			Model:      cfg.LLM.Ollama.Model,
			EmbedModel: cfg.LLM.Ollama.EmbedModel,
			NumCtx:     cfg.LLM.Ollama.NumCtx,
			Timeout:    time.Duration(cfg.LLM.Ollama.TimeoutSeconds) * time.Second,
		})
	case "litellm":
		return litellm.New(litellm.Config{
			BaseURL:    cfg.LLM.LiteLLM.BaseURL,
			Model:      cfg.LLM.LiteLLM.Model,
			EmbedModel: cfg.LLM.LiteLLM.EmbedModel,
			APIKey:     cfg.LLM.LiteLLM.APIKey,
			Timeout:    time.Duration(cfg.LLM.LiteLLM.TimeoutSeconds) * time.Second,
		})
	default:
		return nil, fmt.Errorf("unknown llm.backend %q", cfg.LLM.Backend)
	}
}

func runReindex(args []string) {
	fs := flag.NewFlagSet("reindex", flag.ExitOnError)
	cfgPath := fs.String("config", "", "path to config.yaml")
	_ = fs.Parse(args)
	cfg := loadConfig(*cfgPath)
	log.Printf("reindex stub (Phase 5): db=%s", cfg.Database.Path)
}
