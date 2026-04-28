package config

import (
	"fmt"
	"os"
	"strconv"

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
		LLM: LLM{
			Backend: "ollama",
			Ollama: Ollama{
				Host:           "http://localhost:11434",
				Model:          "qwen3:4b",
				EmbedModel:     "nomic-embed-text",
				NumCtx:         8192,
				TimeoutSeconds: 120,
			},
			LiteLLM: LiteLLM{
				BaseURL:        "http://localhost:4000/v1",
				Model:          "qwen3-4b",
				EmbedModel:     "nomic-embed-text",
				TimeoutSeconds: 120,
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
func Load(path string) (Config, error) {
	cfg := Default()
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("read config %s: %w", path, err)
		}
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return Config{}, fmt.Errorf("parse config %s: %w", path, err)
		}
	}
	applyEnv(&cfg)
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
	// Expand $VAR refs left in YAML (e.g. api_key: "$LITELLM_API_KEY")
	c.LLM.LiteLLM.APIKey = os.ExpandEnv(c.LLM.LiteLLM.APIKey)
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
