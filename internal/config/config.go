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
//
// LLM defaults are intentionally NEUTRAL — empty model names, localhost
// for Ollama, no LiteLLM URL. A fresh `go install` of linklore should
// not auto-target anyone's private gateway; users opt into a real
// backend via configs/config.yaml or env vars (LITELLM_BASE_URL,
// OLLAMA_HOST, LINKLORE_LLM_BACKEND, etc.). The shipped configs/config.yaml
// carries the project-specific values.
func Default() Config {
	return Config{
		Server:   Server{Addr: "127.0.0.1:8080"},
		Database: Database{Path: "./data/linklore.db"},
		LLM: LLM{
			// "none" = run without an LLM. Search degrades to BM25,
			// chat is disabled, ingestion still fetches + extracts.
			Backend: "none",
			LiteLLM: LiteLLM{
				BaseURL:        "",
				Model:          "",
				EmbedModel:     "",
				APIKey:         "",
				TimeoutSeconds: 600,
			},
			Ollama: Ollama{
				Host:           "http://localhost:11434",
				Model:          "",
				EmbedModel:     "",
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

// litellmDefaultAPIKey is the master key our LiteLLM gateway accepts when
// no real key is set. Only consulted when Backend == "litellm".
const litellmDefaultAPIKey = "sk-local"

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
	// Only fill in the LiteLLM master-key fallback when LiteLLM is the
	// active backend. Ollama/none configs should never carry a phantom
	// API key in their dump.
	if cfg.LLM.Backend == "litellm" && cfg.LLM.LiteLLM.APIKey == "" {
		cfg.LLM.LiteLLM.APIKey = litellmDefaultAPIKey
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
	// Model overrides apply to whichever backend is active so the user
	// can switch model without editing YAML.
	if v := os.Getenv("LINKLORE_LLM_MODEL"); v != "" {
		switch c.LLM.Backend {
		case "litellm":
			c.LLM.LiteLLM.Model = v
		case "ollama":
			c.LLM.Ollama.Model = v
		}
	}
	if v := os.Getenv("LINKLORE_LLM_EMBED_MODEL"); v != "" {
		switch c.LLM.Backend {
		case "litellm":
			c.LLM.LiteLLM.EmbedModel = v
		case "ollama":
			c.LLM.Ollama.EmbedModel = v
		}
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
	case "ollama", "litellm", "none":
	default:
		return fmt.Errorf("llm.backend must be ollama, litellm or none, got %q", c.LLM.Backend)
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
