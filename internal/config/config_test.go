package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefault(t *testing.T) {
	c := Default()
	if err := c.Validate(); err != nil {
		t.Fatalf("default config invalid: %v", err)
	}
	if c.Server.Addr != "127.0.0.1:8080" {
		t.Errorf("default addr = %q, want 127.0.0.1:8080", c.Server.Addr)
	}
	// Defaults are intentionally NEUTRAL: a fresh `go install` should
	// not auto-target anyone's private gateway. Backend defaults to
	// "none" (degraded mode) — users opt into ollama or litellm via
	// configs/config.yaml or env vars.
	if c.LLM.Backend != "none" {
		t.Errorf("default backend = %q, want none", c.LLM.Backend)
	}
	if c.LLM.LiteLLM.BaseURL != "" {
		t.Errorf("default litellm base_url = %q, want empty", c.LLM.LiteLLM.BaseURL)
	}
	if c.LLM.Ollama.Host != "http://localhost:11434" {
		t.Errorf("default ollama host = %q, want http://localhost:11434", c.LLM.Ollama.Host)
	}
}

func TestLoad_emptyPath(t *testing.T) {
	clearEnv(t)
	c, err := Load("")
	if err != nil {
		t.Fatalf("Load(\"\"): %v", err)
	}
	if c.Server.Addr != "127.0.0.1:8080" {
		t.Errorf("addr = %q, want 127.0.0.1:8080", c.Server.Addr)
	}
}

func TestLoad_yamlOverrides(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "c.yaml")
	body := `
server:
  addr: ":9999"
database:
  path: "/tmp/x.db"
worker:
  concurrency: 8
  embed_batch_size: 16
  fetch_timeout_seconds: 30
chunking:
  target_tokens: 500
  overlap_tokens: 50
  min_tokens: 20
tags:
  max_per_link: 3
  active_cap: 100
  reuse_distance: 1
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Server.Addr != ":9999" {
		t.Errorf("addr = %q", c.Server.Addr)
	}
	if c.Worker.Concurrency != 8 {
		t.Errorf("concurrency = %d", c.Worker.Concurrency)
	}
	if c.Chunking.TargetTokens != 500 {
		t.Errorf("target_tokens = %d", c.Chunking.TargetTokens)
	}
}

// LLM lives only in env vars now. A stray `llm:` block in yaml must
// be silently ignored so private endpoints can't be smuggled into a
// committed yaml.
func TestLoad_yamlLLMSectionIgnored(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "c.yaml")
	body := `
server:
  addr: ":9999"
database:
  path: "/tmp/x.db"
llm:
  backend: "litellm"
  litellm:
    base_url: "http://snuck-into-yaml/v1"
    api_key: "leaked-key"
worker: {concurrency: 1, embed_batch_size: 1, fetch_timeout_seconds: 1}
chunking: {target_tokens: 10, overlap_tokens: 1, min_tokens: 1}
tags: {max_per_link: 1, active_cap: 1, reuse_distance: 1}
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.LLM.Backend != "none" {
		t.Errorf("yaml llm.backend leaked: %q want %q", c.LLM.Backend, "none")
	}
	if c.LLM.LiteLLM.BaseURL != "" {
		t.Errorf("yaml llm.litellm.base_url leaked: %q", c.LLM.LiteLLM.BaseURL)
	}
	if c.LLM.LiteLLM.APIKey != "" {
		t.Errorf("yaml llm.litellm.api_key leaked: %q", c.LLM.LiteLLM.APIKey)
	}
}

func TestLoad_envOverrides(t *testing.T) {
	clearEnv(t)
	t.Setenv("LINKLORE_ADDR", ":7777")
	t.Setenv("LINKLORE_DB_PATH", "/tmp/env.db")
	// Use the deprecated alias on purpose — Load must canonicalise it
	// to "openai" + the LITELLM_API_KEY var must populate OPENAI side.
	t.Setenv("LINKLORE_LLM_BACKEND", "litellm")
	t.Setenv("OLLAMA_HOST", "http://envhost:11434")
	t.Setenv("LITELLM_API_KEY", "secret")
	t.Setenv("LINKLORE_WORKER_CONCURRENCY", "16")

	c, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Server.Addr != ":7777" {
		t.Errorf("addr = %q", c.Server.Addr)
	}
	if c.Database.Path != "/tmp/env.db" {
		t.Errorf("path = %q", c.Database.Path)
	}
	// "litellm" alias must canonicalise to "openai".
	if c.LLM.Backend != "openai" {
		t.Errorf("backend = %q, want openai (canonical)", c.LLM.Backend)
	}
	if c.LLM.Ollama.Host != "http://envhost:11434" {
		t.Errorf("ollama.host = %q", c.LLM.Ollama.Host)
	}
	// LITELLM_API_KEY (deprecated alias) populates the canonical OpenAI
	// struct; LiteLLM struct is mirrored for back-compat.
	if c.LLM.OpenAI.APIKey != "secret" {
		t.Errorf("openai.apikey = %q", c.LLM.OpenAI.APIKey)
	}
	if c.LLM.LiteLLM.APIKey != "secret" {
		t.Errorf("litellm.apikey alias = %q", c.LLM.LiteLLM.APIKey)
	}
	if c.Worker.Concurrency != 16 {
		t.Errorf("concurrency = %d", c.Worker.Concurrency)
	}
}

func TestLoad_dotEnvFile(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".env")
	body := []byte(`# comment line
LINKLORE_LLM_BACKEND=litellm
LITELLM_API_KEY="from-dotenv"
export LINKLORE_ADDR=':9991'
LINKLORE_DB_PATH=/tmp/from-dotenv.db
`)
	if err := os.WriteFile(envFile, body, 0o600); err != nil {
		t.Fatal(err)
	}
	yamlPath := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(yamlPath, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}

	c, err := Load(yamlPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.LLM.Backend != "openai" {
		t.Errorf("backend from .env (after canonicalise): %q", c.LLM.Backend)
	}
	if c.LLM.OpenAI.APIKey != "from-dotenv" {
		t.Errorf("api key from .env: %q", c.LLM.OpenAI.APIKey)
	}
	if c.Server.Addr != ":9991" {
		t.Errorf("addr from .env: %q", c.Server.Addr)
	}
	if c.Database.Path != "/tmp/from-dotenv.db" {
		t.Errorf("db path from .env: %q", c.Database.Path)
	}
}

func TestLoad_processEnvBeatsDotEnv(t *testing.T) {
	clearEnv(t)
	t.Setenv("LITELLM_API_KEY", "from-shell")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"),
		[]byte("LITELLM_API_KEY=from-dotenv\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	yamlPath := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(yamlPath, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(yamlPath)
	if err != nil {
		t.Fatal(err)
	}
	if c.LLM.LiteLLM.APIKey != "from-shell" {
		t.Errorf("expected shell to win: got %q", c.LLM.LiteLLM.APIKey)
	}
}

// Round-trip a /settings save: the writer must replace existing keys
// in place, append missing ones, leave unrelated lines + comments
// untouched, and round-trip through Load() with the new values.
func TestWriteLLMDotEnv_roundtrips(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")

	// Pre-existing .env has some unrelated lines + an old LLM key we
	// expect to be REPLACED in place (not duplicated).
	initial := `# my header
SOMETHING_ELSE=keep-me
LINKLORE_LLM_BACKEND=ollama
`
	if err := os.WriteFile(envPath, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := Default()
	cfg.LLM.Backend = "openai"
	cfg.LLM.OpenAI.BaseURL = "http://x:8000/v1"
	cfg.LLM.OpenAI.APIKey = "secret # value"
	cfg.LLM.OpenAI.Model = "qwen3"
	cfg.LLM.OpenAI.EmbedModel = "nomic"

	if err := cfg.WriteLLMDotEnv(envPath); err != nil {
		t.Fatalf("WriteLLMDotEnv: %v", err)
	}
	written, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(written)

	// Unrelated lines preserved.
	if !strings.Contains(got, "# my header") || !strings.Contains(got, "SOMETHING_ELSE=keep-me") {
		t.Errorf("unrelated lines lost:\n%s", got)
	}
	// Backend replaced in place — must not appear twice.
	if strings.Count(got, "LINKLORE_LLM_BACKEND=") != 1 {
		t.Errorf("backend not replaced in place:\n%s", got)
	}
	if !strings.Contains(got, "LINKLORE_LLM_BACKEND=openai") {
		t.Errorf("backend not updated:\n%s", got)
	}
	// API key has '#' so must be quoted.
	if !strings.Contains(got, `OPENAI_API_KEY="secret # value"`) {
		t.Errorf("api key not quoted with canonical key name:\n%s", got)
	}

	// Round-trip via Load — yaml is empty, all values come from .env.
	yamlPath := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(yamlPath, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(yamlPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.LLM.Backend != "openai" {
		t.Errorf("loaded backend: %q", loaded.LLM.Backend)
	}
	if loaded.LLM.OpenAI.BaseURL != "http://x:8000/v1" {
		t.Errorf("loaded base_url: %q", loaded.LLM.OpenAI.BaseURL)
	}
	if loaded.LLM.OpenAI.APIKey != "secret # value" {
		t.Errorf("loaded api_key: %q", loaded.LLM.OpenAI.APIKey)
	}
}

func TestLoadDotEnv_quotedValuesAndComments(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	body := `# header
KEY1="hello world"
KEY2='single quoted'
KEY3=plain
# blank below

KEY4="with #hash inside"
`
	p := filepath.Join(dir, ".env")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadDotEnv(p); err != nil {
		t.Fatalf("LoadDotEnv: %v", err)
	}
	cases := map[string]string{
		"KEY1": "hello world",
		"KEY2": "single quoted",
		"KEY3": "plain",
		"KEY4": "with #hash inside",
	}
	for k, want := range cases {
		if got := os.Getenv(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	for _, k := range []string{"KEY1", "KEY2", "KEY3", "KEY4"} {
		_ = os.Unsetenv(k)
	}
}

func TestLoadDotEnv_missingFileIsOK(t *testing.T) {
	if err := LoadDotEnv("/no/such/.env"); err != nil {
		t.Errorf("missing file should be silent, got: %v", err)
	}
}

func TestLoadDotEnv_malformedLineFails(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".env")
	if err := os.WriteFile(p, []byte("no equals sign here\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadDotEnv(p); err == nil {
		t.Error("expected parse error")
	}
}

func TestValidate_errors(t *testing.T) {
	cases := map[string]func(*Config){
		"no addr":     func(c *Config) { c.Server.Addr = "" },
		"no db":       func(c *Config) { c.Database.Path = "" },
		"bad backend": func(c *Config) { c.LLM.Backend = "totally-bogus" },
		"zero conc":   func(c *Config) { c.Worker.Concurrency = 0 },
		"zero batch":  func(c *Config) { c.Worker.EmbedBatchSize = 0 },
		"bad chunk":   func(c *Config) { c.Chunking.OverlapTokens = c.Chunking.TargetTokens },
		"bad tags":    func(c *Config) { c.Tags.MaxPerLink = 0 },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			c := Default()
			mut(&c)
			if err := c.Validate(); err == nil {
				t.Errorf("expected error for %q", name)
			}
		})
	}
}

func TestLoad_badFile(t *testing.T) {
	if _, err := Load("/no/such/path"); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoad_badYAML(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(p, []byte("server: : :\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Fatal("expected parse error")
	}
}

// clearEnv unsets every var Load reads, so tests don't leak through.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"LINKLORE_ADDR", "LINKLORE_DB_PATH", "LINKLORE_LLM_BACKEND",
		"OLLAMA_HOST", "LITELLM_BASE_URL", "LITELLM_API_KEY",
		"LINKLORE_WORKER_CONCURRENCY",
	} {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}
}
