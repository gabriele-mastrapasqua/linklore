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

	"github.com/gabrielemastrapasqua/linklore/internal/archive"
	"github.com/gabrielemastrapasqua/linklore/internal/chat"
	"github.com/gabrielemastrapasqua/linklore/internal/config"
	"github.com/gabrielemastrapasqua/linklore/internal/events"
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
		log.Printf("llm backend disabled: %v — UI runs in BM25-only / no-summary mode", backendErr)
	}
	if backend != nil {
		switch cfg.LLM.Backend {
		case llm.BackendLitellm:
			log.Printf("llm: litellm gateway %s (model=%s, embed=%s, key=%s)",
				cfg.LLM.LiteLLM.BaseURL, cfg.LLM.LiteLLM.Model, cfg.LLM.LiteLLM.EmbedModel,
				maskKey(cfg.LLM.LiteLLM.APIKey))
		case llm.BackendOllama:
			log.Printf("llm: ollama %s (model=%s, embed=%s, num_ctx=%d)",
				cfg.LLM.Ollama.Host, cfg.LLM.Ollama.Model, cfg.LLM.Ollama.EmbedModel,
				cfg.LLM.Ollama.NumCtx)
		}
	}
	eng := search.New(store, backend) // search.New tolerates nil

	broker := events.New()

	// Worker always runs — even with no LLM backend it still drains the
	// fetch queue (pending → fetched). Summary/embed steps short-circuit
	// when w.llm is nil, so links land searchable via BM25-only.
	archiveRoot := ""
	if cfg.Extract.ArchiveHTML {
		archiveRoot = filepath.Join(filepath.Dir(cfg.Database.Path), "snapshots")
	}
	ar, _ := archive.New(archiveRoot)
	wk := worker.New(store, backend, extract.NewFetcher(time.Duration(cfg.Worker.FetchTimeoutSeconds)*time.Second),
		cfg, worker.Options{Archive: ar, Events: broker})
	go func() {
		if err := wk.Run(ctx); err != nil && err != context.Canceled {
			log.Printf("worker exited: %v", err)
		}
	}()

	var chatSvc *chat.Service
	if backend != nil {
		chatSvc = chat.New(store, eng, backend)
	}
	srv, err := server.New(cfg, store, eng, chatSvc, wk, broker)
	if err != nil {
		log.Fatalf("server: %v", err)
	}
	// Wire the config path so /settings can save back to the same YAML
	// file the user passed in. Empty path leaves /settings save in
	// in-memory-only mode.
	resolvedPath := *cfgPath
	if resolvedPath == "" {
		if _, err := os.Stat("./configs/config.yaml"); err == nil {
			resolvedPath = "./configs/config.yaml"
		}
	}
	srv.SetConfigPath(resolvedPath)
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
// when llm.backend == "none" — caller treats this as the "no LLM"
// degraded path (search degrades to BM25, chat is disabled, summaries
// are skipped). A non-nil error also drops into degraded mode but with
// a log line so the user sees why their backend didn't come up.
func newLLMBackend(cfg config.Config) (llm.Backend, error) {
	switch cfg.LLM.Backend {
	case llm.BackendNone, "":
		return nil, nil
	case llm.BackendOllama:
		return ollama.New(ollama.Config{
			Host:       cfg.LLM.Ollama.Host,
			Model:      cfg.LLM.Ollama.Model,
			EmbedModel: cfg.LLM.Ollama.EmbedModel,
			NumCtx:     cfg.LLM.Ollama.NumCtx,
			Timeout:    time.Duration(cfg.LLM.Ollama.TimeoutSeconds) * time.Second,
		})
	case llm.BackendLitellm:
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

// maskKey shows just enough of the API key in logs to confirm it loaded
// without leaking the full value.
func maskKey(k string) string {
	if k == "" {
		return "(empty)"
	}
	if len(k) <= 4 {
		return "***"
	}
	return k[:3] + "***" + k[len(k)-2:]
}

func runReindex(args []string) {
	fs := flag.NewFlagSet("reindex", flag.ExitOnError)
	cfgPath := fs.String("config", "", "path to config.yaml")
	_ = fs.Parse(args)
	cfg := loadConfig(*cfgPath)
	log.Printf("reindex stub (Phase 5): db=%s", cfg.Database.Path)
}
