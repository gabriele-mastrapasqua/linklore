package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the runtime config tree.
//
// Privacy boundary: yaml carries non-secret tunables only (server,
// database, worker, extract, chunking, tags, ui). LLM endpoint + API
// key + models live ONLY in env vars (loaded from .env at startup).
// The yaml file is safe to commit; .env is gitignored. The /settings
// page writes LLM changes to .env, never to yaml.
type Config struct {
	Server    Server    `yaml:"server"`
	Database  Database  `yaml:"database"`
	LLM       LLM       `yaml:"-"` // env-only — see applyEnv + WriteLLMDotEnv
	Worker    Worker    `yaml:"worker"`
	Extract   Extract   `yaml:"extract"`
	Chunking  Chunking  `yaml:"chunking"`
	Tags      Tags      `yaml:"tags"`
	UI        UI        `yaml:"ui"`
	Reminders Reminders `yaml:"reminders"`
}

type Server struct {
	Addr string `yaml:"addr"`
}

type Database struct {
	Path string `yaml:"path"`
}

// LLM is loaded EXCLUSIVELY from env vars — yaml tags are intentionally
// absent so a stray `llm:` block in config.yaml is ignored. The keys
// live in .env (gitignored) so private endpoints + API keys don't end
// up in the public yaml.
type LLM struct {
	Backend string
	Ollama  Ollama
	LiteLLM LiteLLM
}

type Ollama struct {
	Host           string
	Model          string
	EmbedModel     string
	NumCtx         int
	TimeoutSeconds int
}

type LiteLLM struct {
	BaseURL        string
	Model          string
	EmbedModel     string
	APIKey         string
	TimeoutSeconds int
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

// Reminders config (F4). Enabled gates the entire feature — when
// false the bell button stays hidden and /links?due=1 returns nothing.
// DefaultOffset is what the bell quick-action prefills when the user
// clicks without picking a date. Accepts Go duration syntax
// ("168h", "1w" via custom parsing, etc.). Parsing helper lives in
// the server package to keep this struct yaml-friendly.
type Reminders struct {
	Enabled       bool   `yaml:"enabled"`
	DefaultOffset string `yaml:"default_offset"`
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
		Tags:      Tags{MaxPerLink: 5, ActiveCap: 200, ReuseDistance: 1},
		UI:        UI{ReaderFont: "serif", ReaderWidth: "narrow"},
		Reminders: Reminders{Enabled: true, DefaultOffset: "1w"},
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
		if err := yaml.Unmarshal(data, &cfg); err != nil {
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

// SaveYAML writes non-LLM config back to path. LLM is yaml-skipped at
// the struct level, so a saved yaml never carries endpoints or keys.
// Atomic write (.tmp + rename). Mode 0o644 because it has no secrets.
func (c Config) SaveYAML(path string) error {
	if path == "" {
		return fmt.Errorf("save config: empty path")
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	dir := path
	if i := strings.LastIndexAny(dir, "/\\"); i >= 0 {
		dir = dir[:i]
	} else {
		dir = "."
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename %s -> %s: %w", tmp, path, err)
	}
	return nil
}

// llmEnvKeys lists every env var that drives LLM config — single source
// of truth shared by the writer (WriteLLMDotEnv) and the loader
// (applyEnv). Order matters: the writer appends in this order when keys
// are missing from the existing .env, so the file stays grouped sensibly.
var llmEnvKeys = []string{
	"LINKLORE_LLM_BACKEND",
	"LITELLM_BASE_URL",
	"LITELLM_API_KEY",
	"LINKLORE_LLM_MODEL",
	"LINKLORE_LLM_EMBED_MODEL",
	"OLLAMA_HOST",
}

// llmEnvKeysFor returns the keys + values to write for the active
// backend. Keys irrelevant to the backend are omitted so the file
// doesn't grow noisy stubs ("LITELLM_BASE_URL=" when on Ollama).
func (c Config) llmEnvKeysFor() map[string]string {
	out := map[string]string{
		"LINKLORE_LLM_BACKEND": c.LLM.Backend,
	}
	switch c.LLM.Backend {
	case "litellm":
		out["LITELLM_BASE_URL"] = c.LLM.LiteLLM.BaseURL
		out["LITELLM_API_KEY"] = c.LLM.LiteLLM.APIKey
		out["LINKLORE_LLM_MODEL"] = c.LLM.LiteLLM.Model
		out["LINKLORE_LLM_EMBED_MODEL"] = c.LLM.LiteLLM.EmbedModel
	case "ollama":
		out["OLLAMA_HOST"] = c.LLM.Ollama.Host
		out["LINKLORE_LLM_MODEL"] = c.LLM.Ollama.Model
		out["LINKLORE_LLM_EMBED_MODEL"] = c.LLM.Ollama.EmbedModel
	}
	return out
}

// WriteLLMDotEnv updates the LLM-related keys in .env at path. Existing
// occurrences of those keys are replaced in-place; missing ones are
// appended under a "linklore — written by /settings" header. Comments
// and unrelated KEY=VALUE lines are preserved verbatim.
//
// Mode 0o600 because the file can carry LITELLM_API_KEY.
func (c Config) WriteLLMDotEnv(path string) error {
	if path == "" {
		return fmt.Errorf("write .env: empty path")
	}
	managed := c.llmEnvKeysFor()

	var lines []string
	if data, err := os.ReadFile(path); err == nil {
		lines = strings.Split(string(data), "\n")
		// strings.Split on a trailing newline yields a trailing "" element —
		// drop it so we don't double the blank line on rewrite.
		if n := len(lines); n > 0 && lines[n-1] == "" {
			lines = lines[:n-1]
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", path, err)
	}

	seen := map[string]bool{}
	for i, line := range lines {
		body := strings.TrimSpace(line)
		if body == "" || strings.HasPrefix(body, "#") {
			continue
		}
		export := false
		if strings.HasPrefix(body, "export ") {
			export = true
			body = strings.TrimPrefix(body, "export ")
		}
		eq := strings.IndexByte(body, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(body[:eq])
		val, ok := managed[key]
		if !ok {
			continue
		}
		seen[key] = true
		lines[i] = formatEnvLine(key, val, export)
	}

	var missing []string
	for _, k := range llmEnvKeys {
		if val, ok := managed[k]; ok && !seen[k] {
			missing = append(missing, formatEnvLine(k, val, false))
		}
	}
	if len(missing) > 0 {
		if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
			lines = append(lines, "")
		}
		lines = append(lines, "# linklore — written by /settings")
		lines = append(lines, missing...)
	}

	out := strings.Join(lines, "\n") + "\n"
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(out), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename %s -> %s: %w", tmp, path, err)
	}
	return nil
}

// formatEnvLine emits a single KEY=VAL .env line. Values containing
// shell-significant characters (whitespace, '#', quotes) are
// double-quoted with embedded double quotes escaped. The dotenv
// loader strips quotes symmetrically.
func formatEnvLine(key, val string, export bool) string {
	prefix := ""
	if export {
		prefix = "export "
	}
	if strings.ContainsAny(val, " \t#'\"") {
		escaped := strings.ReplaceAll(val, `\`, `\\`)
		escaped = strings.ReplaceAll(escaped, `"`, `\"`)
		return prefix + key + `="` + escaped + `"`
	}
	return prefix + key + "=" + val
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
