package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server    Server    `yaml:"server"`
	Database  Database  `yaml:"database"`
	LLM       LLM       `yaml:"llm"`
	Worker    Worker    `yaml:"worker"`
	Extract   Extract   `yaml:"extract"`
	Chunking  Chunking  `yaml:"chunking"`
	Tags      Tags      `yaml:"tags"`
	UI        UI        `yaml:"ui"`
}

type Server struct {
	Addr string `yaml:"addr"`
}

type Database struct {
	Path string `yaml:"path"`
}

type LLM struct {
	Backend string  `yaml:"backend"`
	Ollama  Ollama  `yaml:"ollama"`
	LiteLLM LiteLLM `yaml:"litellm"`
}

type Ollama struct {
	Host           string `yaml:"host"`
	Model          string `yaml:"model"`
	EmbedModel     string `yaml:"embed_model"`
	NumCtx         int    `yaml:"num_ctx"`
	TimeoutSeconds int    `yaml:"timeout_seconds"`
}

type LiteLLM struct {
	BaseURL        string `yaml:"base_url"`
	Model          string `yaml:"model"`
	EmbedModel     string `yaml:"embed_model"`
	APIKey         string `yaml:"api_key"`
	TimeoutSeconds int    `yaml:"timeout_seconds"`
}

type Worker struct {
	Concurrency         int `yaml:"concurrency"`
	EmbedBatchSize      int `yaml:"embed_batch_size"`
	FetchTimeoutSeconds int `yaml:"fetch_timeout_seconds"`
}

type Extract struct {
	HeadlessFallback bool `yaml:"headless_fallback"`
	ArchiveHTML      bool `yaml:"archive_html"`
	MinReadableChars int  `yaml:"min_readable_chars"`
}

type Chunking struct {
	TargetTokens  int `yaml:"target_tokens"`
	OverlapTokens int `yaml:"overlap_tokens"`
	MinTokens     int `yaml:"min_tokens"`
}

type Tags struct {
	MaxPerLink    int `yaml:"max_per_link"`
	ActiveCap     int `yaml:"active_cap"`
	ReuseDistance int `yaml:"reuse_distance"`
}

type UI struct {
	ShowImagesDefault bool   `yaml:"show_images_default"`
	ReaderFont        string `yaml:"reader_font"`
	ReaderWidth       string `yaml:"reader_width"`
}

// Default returns a Config populated with sensible defaults.
// Used as the base before YAML/env overrides.
func Default() Config {
	return Config{
		Server:   Server{Addr: "127.0.0.1:8080"},
		Database: Database{Path: "./data/linklore.db"},
		// Default to litellm (in front of vLLM on the DGX) — same stack
		// graphrag uses, materially faster than Ollama for our workload.
		// API key default is "sk-local": that's the master key the DGX
		// Spark gateway is configured with, per graphrag's config comment
		// ("APIKey defaults to sk-local in our infra"). Real keys live
		// in .env / process env and override this.
		LLM: LLM{
			Backend: "litellm",
			LiteLLM: LiteLLM{
				BaseURL:        "http://192.168.1.94:8000/v1",
				Model:          "qwen36-chat",
				EmbedModel:     "nomic-embed",
				APIKey:         "sk-local",
				TimeoutSeconds: 600,
			},
			Ollama: Ollama{
				Host:           "http://192.168.1.94:11434",
				Model:          "qwen3.6:35b",
				EmbedModel:     "nomic-embed-text",
				NumCtx:         32768,
				TimeoutSeconds: 600,
			},
		},
		Worker:   Worker{Concurrency: 4, EmbedBatchSize: 32, FetchTimeoutSeconds: 15},
		Extract:  Extract{MinReadableChars: 200},
		Chunking: Chunking{TargetTokens: 800, OverlapTokens: 100, MinTokens: 40},
		Tags:     Tags{MaxPerLink: 5, ActiveCap: 200, ReuseDistance: 1},
		UI:       UI{ReaderFont: "serif", ReaderWidth: "narrow"},
	}
}

// Load reads a YAML config from path, falls back to defaults for missing
// fields, then applies env overrides. An empty path returns Default()+env.
//
// Before everything else it tries to load a .env file from (in order):
//
//  1. ./.env
//  2. <dir of the config file>/.env (when path != "")
//
// Anything already set in the process env wins, so a one-shot
// `LITELLM_API_KEY=… ./linklore serve` still overrides the .env file.
func Load(path string) (Config, error) {
	// .env is best-effort: parsing failures are fatal, "no such file" is fine.
	if err := LoadDotEnv(".env"); err != nil {
		return Config{}, err
	}
	if path != "" {
		dir := path
		if i := strings.LastIndexAny(dir, "/\\"); i >= 0 {
			dir = dir[:i]
		} else {
			dir = "."
		}
		if err := LoadDotEnv(dir + "/.env"); err != nil {
			return Config{}, err
		}
	}

	cfg := Default()
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("read config %s: %w", path, err)
		}
		// Expand ${VAR} / $VAR refs in the YAML against the (now loaded) env
		// so api_key: "${LITELLM_API_KEY}" lands in the parsed struct directly.
		expanded := os.ExpandEnv(string(data))
		if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
			return Config{}, fmt.Errorf("parse config %s: %w", path, err)
		}
	}
	applyEnv(&cfg)
	// LiteLLM gateway in our infra accepts "sk-local" as a master key when
	// no real key is supplied. Falling back here means a fresh checkout
	// works against the DGX Spark out of the box; users with an actual
	// per-account key just set LITELLM_API_KEY and override this.
	if cfg.LLM.LiteLLM.APIKey == "" {
		cfg.LLM.LiteLLM.APIKey = "sk-local"
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func applyEnv(c *Config) {
	if v := os.Getenv("LINKLORE_ADDR"); v != "" {
		c.Server.Addr = v
	}
	if v := os.Getenv("LINKLORE_DB_PATH"); v != "" {
		c.Database.Path = v
	}
	if v := os.Getenv("LINKLORE_LLM_BACKEND"); v != "" {
		c.LLM.Backend = v
	}
	if v := os.Getenv("OLLAMA_HOST"); v != "" {
		c.LLM.Ollama.Host = v
	}
	if v := os.Getenv("LITELLM_BASE_URL"); v != "" {
		c.LLM.LiteLLM.BaseURL = v
	}
	if v := os.Getenv("LITELLM_API_KEY"); v != "" {
		c.LLM.LiteLLM.APIKey = v
	}
	if v := os.Getenv("LINKLORE_WORKER_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.Worker.Concurrency = n
		}
	}
}

func (c Config) Validate() error {
	if c.Server.Addr == "" {
		return fmt.Errorf("server.addr required")
	}
	if c.Database.Path == "" {
		return fmt.Errorf("database.path required")
	}
	switch c.LLM.Backend {
	case "ollama", "litellm":
	default:
		return fmt.Errorf("llm.backend must be ollama or litellm, got %q", c.LLM.Backend)
	}
	if c.Worker.Concurrency <= 0 {
		return fmt.Errorf("worker.concurrency must be > 0")
	}
	if c.Worker.EmbedBatchSize <= 0 {
		return fmt.Errorf("worker.embed_batch_size must be > 0")
	}
	if c.Chunking.TargetTokens <= 0 || c.Chunking.OverlapTokens < 0 ||
		c.Chunking.OverlapTokens >= c.Chunking.TargetTokens {
		return fmt.Errorf("invalid chunking sizes: target=%d overlap=%d",
			c.Chunking.TargetTokens, c.Chunking.OverlapTokens)
	}
	if c.Tags.MaxPerLink <= 0 || c.Tags.ActiveCap <= 0 {
		return fmt.Errorf("invalid tag caps")
	}
	return nil
}
