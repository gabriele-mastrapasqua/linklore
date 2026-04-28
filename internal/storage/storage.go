// Package storage owns the SQLite layer: connection setup, schema migrations,
// and CRUD for collections, links, chunks, and tags.
package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Store wraps a *sql.DB plus the open path so we can log it back.
type Store struct {
	db   *sql.DB
	path string
}

// Open initialises a SQLite database at path with WAL + FK enforcement.
// Use ":memory:" for tests; in that case WAL is skipped automatically.
func Open(ctx context.Context, path string) (*Store, error) {
	dsn := buildDSN(path)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	// Single writer connection avoids "database is locked" on busy WAL hot paths.
	// Multiple readers are still fine via the same handle.
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	pragmas := []string{
		"PRAGMA synchronous=NORMAL",
		"PRAGMA cache_size=-64000",
		"PRAGMA mmap_size=268435456",
	}
	for _, p := range pragmas {
		if _, err := db.ExecContext(ctx, p); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("%s: %w", p, err)
		}
	}
	s := &Store{db: db, path: path}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func buildDSN(path string) string {
	if path == ":memory:" {
		return "file::memory:?cache=shared&_journal_mode=DELETE&_foreign_keys=on"
	}
	q := url.Values{}
	q.Set("_journal_mode", "WAL")
	q.Set("_busy_timeout", "5000")
	q.Set("_txlock", "immediate")
	q.Set("_foreign_keys", "on")
	return "file:" + path + "?" + q.Encode()
}

func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) migrate(ctx context.Context) error {
	for i, stmt := range migrations {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migration %d: %w\n--SQL--\n%s", i, err, stmt)
		}
	}
	return nil
}

// ---- domain types ----

type Collection struct {
	ID          int64
	Slug        string
	Name        string
	Description string
	CreatedAt   time.Time
}

type Link struct {
	ID           int64
	CollectionID int64
	URL          string
	Title        string
	Description  string
	ImageURL     string
	ContentMD    string
	ContentLang  string
	Summary      string
	Status       string
	ReadAt       *time.Time
	FetchError   string
	ArchivePath  string
	FetchedAt    *time.Time
	CreatedAt    time.Time
}

type Chunk struct {
	ID        int64
	LinkID    int64
	Ord       int
	Text      string
	Embedding []byte
}

type Tag struct {
	ID   int64
	Slug string
	Name string
}

const (
	StatusPending     = "pending"
	StatusFetched     = "fetched"
	StatusSummarized  = "summarized"
	StatusFailed      = "failed"

	TagSourceAuto = "auto"
	TagSourceUser = "user"
)

// ErrNotFound is returned when a SELECT-by-id finds nothing.
var ErrNotFound = errors.New("not found")

// ---- Collections ----

func (s *Store) CreateCollection(ctx context.Context, slug, name, description string) (*Collection, error) {
	slug = strings.TrimSpace(slug)
	if slug == "" || name == "" {
		return nil, fmt.Errorf("slug and name required")
	}
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO collections(slug, name, description, created_at) VALUES (?, ?, ?, ?)`,
		slug, name, description, now.Unix())
	if err != nil {
		return nil, fmt.Errorf("insert collection: %w", err)
	}
	id, _ := res.LastInsertId()
	return &Collection{ID: id, Slug: slug, Name: name, Description: description, CreatedAt: now}, nil
}

func (s *Store) GetCollectionBySlug(ctx context.Context, slug string) (*Collection, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, slug, name, COALESCE(description,''), created_at FROM collections WHERE slug = ?`, slug)
	var c Collection
	var ts int64
	if err := row.Scan(&c.ID, &c.Slug, &c.Name, &c.Description, &ts); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scan collection: %w", err)
	}
	c.CreatedAt = time.Unix(ts, 0).UTC()
	return &c, nil
}

func (s *Store) ListCollections(ctx context.Context) ([]Collection, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, slug, name, COALESCE(description,''), created_at FROM collections ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Collection
	for rows.Next() {
		var c Collection
		var ts int64
		if err := rows.Scan(&c.ID, &c.Slug, &c.Name, &c.Description, &ts); err != nil {
			return nil, err
		}
		c.CreatedAt = time.Unix(ts, 0).UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) DeleteCollection(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM collections WHERE id = ?`, id)
	return err
}

// ---- Links ----

func (s *Store) CreateLink(ctx context.Context, collectionID int64, urlStr string) (*Link, error) {
	urlStr = strings.TrimSpace(urlStr)
	if urlStr == "" {
		return nil, fmt.Errorf("url required")
	}
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO links(collection_id, url, status, created_at) VALUES (?, ?, ?, ?)`,
		collectionID, urlStr, StatusPending, now.Unix())
	if err != nil {
		return nil, fmt.Errorf("insert link: %w", err)
	}
	id, _ := res.LastInsertId()
	return &Link{ID: id, CollectionID: collectionID, URL: urlStr, Status: StatusPending, CreatedAt: now}, nil
}

func (s *Store) GetLink(ctx context.Context, id int64) (*Link, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, collection_id, url,
		        COALESCE(title,''), COALESCE(description,''), COALESCE(image_url,''),
		        COALESCE(content_md,''), COALESCE(content_lang,''), COALESCE(summary,''),
		        status, read_at, COALESCE(fetch_error,''), COALESCE(archive_path,''),
		        fetched_at, created_at
		 FROM links WHERE id = ?`, id)
	return scanLink(row.Scan)
}

func (s *Store) ListLinksByCollection(ctx context.Context, collectionID int64, limit, offset int) ([]Link, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, collection_id, url,
		        COALESCE(title,''), COALESCE(description,''), COALESCE(image_url,''),
		        COALESCE(content_md,''), COALESCE(content_lang,''), COALESCE(summary,''),
		        status, read_at, COALESCE(fetch_error,''), COALESCE(archive_path,''),
		        fetched_at, created_at
		 FROM links WHERE collection_id = ? ORDER BY created_at DESC LIMIT ? OFFSET ?`,
		collectionID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Link
	for rows.Next() {
		l, err := scanLink(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	return out, rows.Err()
}

// scanLink works for both *sql.Row and *sql.Rows by accepting their Scan func.
func scanLink(scan func(...any) error) (*Link, error) {
	var l Link
	var readAt, fetchedAt sql.NullInt64
	var createdAt int64
	err := scan(&l.ID, &l.CollectionID, &l.URL,
		&l.Title, &l.Description, &l.ImageURL,
		&l.ContentMD, &l.ContentLang, &l.Summary,
		&l.Status, &readAt, &l.FetchError, &l.ArchivePath,
		&fetchedAt, &createdAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if readAt.Valid {
		t := time.Unix(readAt.Int64, 0).UTC()
		l.ReadAt = &t
	}
	if fetchedAt.Valid {
		t := time.Unix(fetchedAt.Int64, 0).UTC()
		l.FetchedAt = &t
	}
	l.CreatedAt = time.Unix(createdAt, 0).UTC()
	return &l, nil
}

// UpdateLinkExtraction persists the extraction outputs and bumps status.
func (s *Store) UpdateLinkExtraction(ctx context.Context, id int64, title, desc, imageURL, contentMD, lang, archivePath string) error {
	now := time.Now().UTC().Unix()
	_, err := s.db.ExecContext(ctx,
		`UPDATE links
		   SET title = ?, description = ?, image_url = ?, content_md = ?,
		       content_lang = ?, archive_path = ?, status = ?, fetched_at = ?, fetch_error = ''
		 WHERE id = ?`,
		title, desc, imageURL, contentMD, lang, archivePath, StatusFetched, now, id)
	return err
}

// UpdateLinkSummary sets the LLM summary and marks the link summarised.
func (s *Store) UpdateLinkSummary(ctx context.Context, id int64, summary string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE links SET summary = ?, status = ? WHERE id = ?`,
		summary, StatusSummarized, id)
	return err
}

// MarkLinkFailed records the error and flips status to "failed".
func (s *Store) MarkLinkFailed(ctx context.Context, id int64, errStr string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE links SET status = ?, fetch_error = ? WHERE id = ?`,
		StatusFailed, errStr, id)
	return err
}

// MarkLinkRead sets read_at to now (Inbox marks-as-read action).
func (s *Store) MarkLinkRead(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE links SET read_at = ? WHERE id = ?`, time.Now().UTC().Unix(), id)
	return err
}

func (s *Store) DeleteLink(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM links WHERE id = ?`, id)
	return err
}

// ---- Chunks ----

func (s *Store) InsertChunks(ctx context.Context, linkID int64, texts []string) ([]int64, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO chunks(link_id, ord, text) VALUES (?, ?, ?)`)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	ids := make([]int64, 0, len(texts))
	for i, t := range texts {
		res, err := stmt.ExecContext(ctx, linkID, i, t)
		if err != nil {
			return nil, err
		}
		id, _ := res.LastInsertId()
		ids = append(ids, id)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return ids, nil
}

func (s *Store) SetChunkEmbedding(ctx context.Context, chunkID int64, blob []byte) error {
	_, err := s.db.ExecContext(ctx, `UPDATE chunks SET embedding = ? WHERE id = ?`, blob, chunkID)
	return err
}

func (s *Store) ListChunksByLink(ctx context.Context, linkID int64) ([]Chunk, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, link_id, ord, text, embedding FROM chunks WHERE link_id = ? ORDER BY ord`, linkID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Chunk
	for rows.Next() {
		var c Chunk
		if err := rows.Scan(&c.ID, &c.LinkID, &c.Ord, &c.Text, &c.Embedding); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ---- Tags ----

// UpsertTag creates the tag if missing (slug-keyed) and returns it.
func (s *Store) UpsertTag(ctx context.Context, slug, name string) (*Tag, error) {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return nil, fmt.Errorf("slug required")
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO tags(slug, name) VALUES (?, ?) ON CONFLICT(slug) DO NOTHING`,
		slug, name); err != nil {
		return nil, err
	}
	row := s.db.QueryRowContext(ctx, `SELECT id, slug, name FROM tags WHERE slug = ?`, slug)
	var t Tag
	if err := row.Scan(&t.ID, &t.Slug, &t.Name); err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *Store) AttachTag(ctx context.Context, linkID, tagID int64, source string) error {
	if source != TagSourceAuto && source != TagSourceUser {
		return fmt.Errorf("invalid tag source %q", source)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO link_tags(link_id, tag_id, source) VALUES (?, ?, ?)
		 ON CONFLICT(link_id, tag_id) DO UPDATE SET source = excluded.source`,
		linkID, tagID, source)
	return err
}

func (s *Store) DetachTag(ctx context.Context, linkID, tagID int64) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM link_tags WHERE link_id = ? AND tag_id = ?`, linkID, tagID)
	return err
}

func (s *Store) ListTagsByLink(ctx context.Context, linkID int64) ([]Tag, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT t.id, t.slug, t.name FROM tags t
		   JOIN link_tags lt ON lt.tag_id = t.id
		  WHERE lt.link_id = ? ORDER BY t.name`, linkID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Tag
	for rows.Next() {
		var t Tag
		if err := rows.Scan(&t.ID, &t.Slug, &t.Name); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ListTopTagSlugs returns up to limit tag slugs with their usage count desc.
// Used by summarize to bias the LLM towards reusing existing tags.
func (s *Store) ListTopTagSlugs(ctx context.Context, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT t.slug FROM tags t
		   JOIN link_tags lt ON lt.tag_id = t.id
		   GROUP BY t.id ORDER BY COUNT(*) DESC, t.slug ASC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// CountActiveTags returns how many tags have at least one link attached.
// Used to enforce the global active-tag cap (Tags.ActiveCap).
func (s *Store) CountActiveTags(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT tag_id) FROM link_tags`).Scan(&n)
	return n, err
}

// ---- search helpers ----

// FTSHit is a candidate produced by an FTS5 MATCH; bm25 is lower=better.
type FTSHit struct {
	LinkID  int64
	ChunkID int64 // 0 when the hit is link-level (links_fts)
	BM25    float64
	Snippet string
}

// SearchLinksFTS runs an FTS5 MATCH over links_fts and returns the top-N hits.
// query is passed to FTS5 verbatim — callers should sanitise.
func (s *Store) SearchLinksFTS(ctx context.Context, query string, limit int) ([]FTSHit, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT links.id, bm25(links_fts), snippet(links_fts, 2, '[', ']', '…', 16)
		  FROM links_fts
		  JOIN links ON links.id = links_fts.rowid
		 WHERE links_fts MATCH ?
		 ORDER BY bm25(links_fts) ASC
		 LIMIT ?`, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FTSHit
	for rows.Next() {
		var h FTSHit
		if err := rows.Scan(&h.LinkID, &h.BM25, &h.Snippet); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// SearchChunksFTS runs an FTS5 MATCH over chunks_fts. Optionally narrows the
// hit set to a single collection when collectionID > 0.
func (s *Store) SearchChunksFTS(ctx context.Context, query string, collectionID int64, limit int) ([]FTSHit, error) {
	if limit <= 0 {
		limit = 50
	}
	var (
		rows *sql.Rows
		err  error
	)
	if collectionID > 0 {
		rows, err = s.db.QueryContext(ctx, `
			SELECT chunks.link_id, chunks.id, bm25(chunks_fts), snippet(chunks_fts, 0, '[', ']', '…', 16)
			  FROM chunks_fts
			  JOIN chunks ON chunks.id = chunks_fts.rowid
			  JOIN links  ON links.id = chunks.link_id
			 WHERE chunks_fts MATCH ? AND links.collection_id = ?
			 ORDER BY bm25(chunks_fts) ASC
			 LIMIT ?`, query, collectionID, limit)
	} else {
		rows, err = s.db.QueryContext(ctx, `
			SELECT chunks.link_id, chunks.id, bm25(chunks_fts), snippet(chunks_fts, 0, '[', ']', '…', 16)
			  FROM chunks_fts
			  JOIN chunks ON chunks.id = chunks_fts.rowid
			 WHERE chunks_fts MATCH ?
			 ORDER BY bm25(chunks_fts) ASC
			 LIMIT ?`, query, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FTSHit
	for rows.Next() {
		var h FTSHit
		if err := rows.Scan(&h.LinkID, &h.ChunkID, &h.BM25, &h.Snippet); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// ListChunksByCollection returns every embedded chunk in a collection, used
// by the vector re-rank step. It only returns rows that already have a
// non-empty embedding — chunks still pending embed are skipped.
func (s *Store) ListChunksByCollection(ctx context.Context, collectionID int64) ([]Chunk, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id, c.link_id, c.ord, c.text, c.embedding
		  FROM chunks c
		  JOIN links  l ON l.id = c.link_id
		 WHERE l.collection_id = ? AND c.embedding IS NOT NULL`, collectionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Chunk
	for rows.Next() {
		var c Chunk
		if err := rows.Scan(&c.ID, &c.LinkID, &c.Ord, &c.Text, &c.Embedding); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetChunksByIDs fetches chunks by ID in arbitrary order. Helper for the
// search pipeline once it has narrowed the candidate set.
func (s *Store) GetChunksByIDs(ctx context.Context, ids []int64) ([]Chunk, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, link_id, ord, text, embedding
		  FROM chunks WHERE id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Chunk
	for rows.Next() {
		var c Chunk
		if err := rows.Scan(&c.ID, &c.LinkID, &c.Ord, &c.Text, &c.Embedding); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
