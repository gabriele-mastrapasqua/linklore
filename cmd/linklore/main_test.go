// Tests for the cmd/linklore wiring helpers. The big blockers — runServe
// and runAdd — both call log.Fatalf on errors, which can't be unit-tested
// in-process without subprocess gymnastics. We cover the pure helpers
// plus the subset of runServe / runAdd that's safely callable.

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gabriele-mastrapasqua/linklore/internal/config"
	"github.com/gabriele-mastrapasqua/linklore/internal/llm"
)

// binPath returns the path to drop a freshly-built `linklore` binary
// in a test temp dir, with the .exe suffix on Windows. `go build -o foo`
// silently produces foo.exe on windows; without the suffix, exec.Command
// can't resolve the binary.
func binPath(t *testing.T) string {
	t.Helper()
	name := "linklore"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(t.TempDir(), name)
}

// ---------- maskKey ----------

func TestMaskKey(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "(empty)"},
		{"a", "***"},
		{"ab", "***"},
		{"abc", "***"},
		{"abcd", "***"},
		{"abcde", "abc***de"},
		{"sk-local", "sk-***al"},
		{"sk-1234567890", "sk-***90"},
	}
	for _, c := range cases {
		if got := maskKey(c.in); got != c.want {
			t.Errorf("maskKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ---------- newLLMBackend ----------

func TestNewLLMBackend_noneReturnsNil(t *testing.T) {
	cfg := config.Default()
	cfg.LLM.Backend = llm.BackendNone
	b, err := newLLMBackend(cfg)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if b != nil {
		t.Errorf("expected nil backend for 'none', got %T", b)
	}
}

func TestNewLLMBackend_emptyTreatedAsNone(t *testing.T) {
	// A YAML file that omits llm.backend leaves the string empty;
	// the binary must still boot in degraded mode rather than crash.
	cfg := config.Default()
	cfg.LLM.Backend = ""
	b, err := newLLMBackend(cfg)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if b != nil {
		t.Errorf("expected nil backend for empty backend, got %T", b)
	}
}

func TestNewLLMBackend_ollama(t *testing.T) {
	cfg := config.Default()
	cfg.LLM.Backend = llm.BackendOllama
	cfg.LLM.Ollama.Host = "http://localhost:11434"
	cfg.LLM.Ollama.Model = "qwen3:4b"
	cfg.LLM.Ollama.EmbedModel = "nomic-embed-text"
	b, err := newLLMBackend(cfg)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if b == nil {
		t.Errorf("expected non-nil ollama backend")
	}
}

func TestNewLLMBackend_litellmRejectsEmptyURL(t *testing.T) {
	cfg := config.Default()
	cfg.LLM.Backend = llm.BackendLitellm
	cfg.LLM.LiteLLM.BaseURL = ""
	if _, err := newLLMBackend(cfg); err == nil {
		t.Error("expected error when litellm base_url is empty")
	}
}

func TestNewLLMBackend_unknownReturnsError(t *testing.T) {
	cfg := config.Default()
	cfg.LLM.Backend = "claude-direct"
	if _, err := newLLMBackend(cfg); err == nil {
		t.Error("expected error for unknown backend")
	}
}

// ---------- loadConfig (success path) ----------

func TestLoadConfig_readsFromGivenPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
server:
  addr: ":7777"
database:
  path: "/tmp/test.db"
llm:
  backend: "none"
worker:
  concurrency: 2
  embed_batch_size: 4
  fetch_timeout_seconds: 5
chunking:
  target_tokens: 800
  overlap_tokens: 100
  min_tokens: 40
tags:
  max_per_link: 5
  active_cap: 200
  reuse_distance: 1
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got := loadConfig(path)
	if got.Server.Addr != ":7777" {
		t.Errorf("addr = %q", got.Server.Addr)
	}
	if got.LLM.Backend != "none" {
		t.Errorf("backend = %q", got.LLM.Backend)
	}
}

func TestLoadConfig_emptyPathFallsBackToDefault(t *testing.T) {
	// loadConfig("") triggers the "look for ./configs/config.yaml" probe
	// which we deliberately steer past by changing into a tempdir that
	// has no such file, so the call is forced to use Default()+env.
	dir := t.TempDir()
	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	got := loadConfig("")
	if got.Server.Addr == "" {
		t.Errorf("loadConfig(empty) returned zero-value config")
	}
}

// ---------- openStore ----------

func TestOpenStore_createsDirAndOpens(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "nested", "linklore.db")
	st := openStore(context.Background(), dbPath)
	defer func() { _ = st.Close() }()
	if _, err := os.Stat(dbPath); err != nil {
		t.Errorf("db file not created: %v", err)
	}
}

// ---------- subprocess: dispatcher behaviour ----------

// TestUsageOnNoArgs builds the actual binary in a tempdir and runs it
// with no arguments — must exit 2 and print the usage banner. Catches
// regressions in cmd/linklore/main.go's argument dispatcher.
func TestUsageOnNoArgs(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess build in -short mode")
	}
	bin := binPath(t)
	build := exec.Command("go", "build", "-tags=sqlite_fts5", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	cmd := exec.Command(bin)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Errorf("expected non-zero exit, got success: %s", out)
	}
	if !strings.Contains(string(out), "linklore - local-first link manager") {
		t.Errorf("missing usage banner: %s", out)
	}
}

func TestUnknownSubcommandFails(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess build in -short mode")
	}
	bin := binPath(t)
	build := exec.Command("go", "build", "-tags=sqlite_fts5", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	cmd := exec.Command(bin, "frobnicate")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Errorf("expected non-zero exit for unknown subcommand: %s", out)
	}
}

func TestHelpFlagSucceeds(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess build in -short mode")
	}
	bin := binPath(t)
	if out, err := exec.Command("go", "build", "-tags=sqlite_fts5", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	out, err := exec.Command(bin, "--help").CombinedOutput()
	if err != nil {
		t.Errorf("--help should succeed, err=%v out=%s", err, out)
	}
	if !strings.Contains(string(out), "linklore <subcommand>") {
		t.Errorf("--help missing usage line: %s", out)
	}
}

// ---------- runAdd (subprocess) ----------

// TestRunReindex_subprocessLogsAndExits builds the binary and invokes
// the reindex stub, expecting clean exit + the expected log line.
func TestRunReindex_subprocessLogsAndExits(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess build in -short mode")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "linklore")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if out, err := exec.Command("go", "build", "-tags=sqlite_fts5", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	cmd := exec.Command(bin, "reindex")
	cmd.Env = append(os.Environ(),
		"LINKLORE_DB_PATH="+filepath.Join(dir, "test.db"),
		"LINKLORE_LLM_BACKEND=none",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("reindex: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "reindex stub") {
		t.Errorf("expected stub log line, got: %s", out)
	}
}

// TestRunAdd_subprocessNoArgsFails covers the error branch in runAdd
// without involving in-process log.Fatal.
func TestRunAdd_subprocessNoArgsFails(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess build in -short mode")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "linklore")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if out, err := exec.Command("go", "build", "-tags=sqlite_fts5", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	cmd := exec.Command(bin, "add") // no URL arg
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Errorf("expected non-zero exit when URL is missing, got: %s", out)
	}
	if !strings.Contains(string(out), "usage: linklore add URL") {
		t.Errorf("missing usage hint: %s", out)
	}
}

// TestRunAdd_subprocessQueuesLink builds the binary, runs `linklore add`
// against a fresh database path, and asserts a row materialised. This
// is the only end-to-end check for the CLI ingest path — runAdd uses
// log.Fatalf which can't be tested in-process.
func TestRunAdd_subprocessQueuesLink(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess build in -short mode")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "linklore")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if out, err := exec.Command("go", "build", "-tags=sqlite_fts5", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	dbPath := filepath.Join(dir, "test.db")
	// flag.NewFlagSet stops parsing at the first positional, so flags
	// must precede the URL on the command line.
	cmd := exec.Command(bin, "add", "-c", "test", "https://example.com/x")
	cmd.Env = append(os.Environ(),
		"LINKLORE_DB_PATH="+dbPath,
		"LINKLORE_LLM_BACKEND=none",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "queued link id=") {
		t.Errorf("missing 'queued link' confirmation: %s", out)
	}
	if !strings.Contains(string(out), "collection=test") {
		t.Errorf("collection slug missing from output: %s", out)
	}
	// File got created — the most reliable side-effect to assert.
	if _, err := os.Stat(dbPath); err != nil {
		t.Errorf("db not created at %s: %v", dbPath, err)
	}
}
