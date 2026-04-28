package archive

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStore_disabledIsNoop(t *testing.T) {
	s, err := New("")
	if err != nil {
		t.Fatal(err)
	}
	if s.Enabled() {
		t.Error("expected disabled")
	}
	path, err := s.Save(1, "<html/>")
	if err != nil || path != "" {
		t.Errorf("disabled save: %q %v", path, err)
	}
}

func TestStore_saveAndLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	s, err := New(filepath.Join(dir, "snapshots"))
	if err != nil {
		t.Fatal(err)
	}
	html := "<html><body>hello world</body></html>"
	path, err := s.Save(42, html)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(path, "42.html.gz") {
		t.Errorf("path = %q", path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file missing: %v", err)
	}
	got, err := s.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != html {
		t.Errorf("roundtrip mismatch: %q", got)
	}
}

func TestStore_emptyHTMLRejected(t *testing.T) {
	s, _ := New(t.TempDir())
	if _, err := s.Save(1, ""); err == nil {
		t.Error("expected error")
	}
}

func TestStore_deleteIdempotent(t *testing.T) {
	s, _ := New(t.TempDir())
	if err := s.Delete(""); err != nil {
		t.Errorf("empty path should be noop: %v", err)
	}
	if err := s.Delete(filepath.Join(t.TempDir(), "missing.gz")); err != nil {
		t.Errorf("missing file should be noop: %v", err)
	}
	path, _ := s.Save(7, "<html/>")
	if err := s.Delete(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file not removed: %v", err)
	}
}

func TestStore_compressionShrinks(t *testing.T) {
	s, _ := New(t.TempDir())
	body := strings.Repeat("the quick brown fox jumps over the lazy dog ", 200) // ~9 KiB
	path, err := s.Save(1, body)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Size() >= int64(len(body)) {
		t.Errorf("not compressed: %d vs %d", info.Size(), len(body))
	}
}
