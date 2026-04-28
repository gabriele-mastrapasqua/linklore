// Package archive persists optional gzipped HTML snapshots so a saved link
// stays readable even after the upstream URL goes 404.
//
// Disabled by default (extract.archive_html: false). When enabled the worker
// writes <root>/<id>.html.gz; the path is stored in links.archive_path so
// lookups don't have to scan the filesystem.
package archive

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
)

// Store owns a directory of gzipped HTML snapshots.
type Store struct {
	root string
}

// New creates the snapshot directory if missing and returns a *Store.
// An empty root disables archiving — Save returns ("", nil) on a no-op.
func New(root string) (*Store, error) {
	if root == "" {
		return &Store{}, nil
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir snapshots: %w", err)
	}
	return &Store{root: root}, nil
}

// Enabled reports whether the store will actually write anything.
func (s *Store) Enabled() bool { return s != nil && s.root != "" }

// Save gzips html to <root>/<linkID>.html.gz and returns the absolute path.
// On a disabled store returns ("", nil) so callers can wire it
// unconditionally.
func (s *Store) Save(linkID int64, html string) (string, error) {
	if !s.Enabled() {
		return "", nil
	}
	if html == "" {
		return "", errors.New("empty html")
	}
	path := filepath.Join(s.root, strconv.FormatInt(linkID, 10)+".html.gz")
	f, err := os.Create(path)
	if err != nil {
		return "", fmt.Errorf("create snapshot: %w", err)
	}
	gz := gzip.NewWriter(f)
	if _, err := io.WriteString(gz, html); err != nil {
		_ = gz.Close()
		_ = f.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("gzip write: %w", err)
	}
	if err := gz.Close(); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("gzip close: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("close: %w", err)
	}
	return path, nil
}

// Load returns the decompressed HTML at path, or an error if missing/corrupt.
func (s *Store) Load(path string) (string, error) {
	if path == "" {
		return "", errors.New("no archive path")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", err
	}
	defer gz.Close()
	body, err := io.ReadAll(gz)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// Delete removes a snapshot file. Missing files are not an error — this is
// a GC sweep helper, not a strict precondition.
func (s *Store) Delete(path string) error {
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
