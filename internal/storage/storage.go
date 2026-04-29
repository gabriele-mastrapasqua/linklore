// Package storage owns the SQLite layer: connection setup, schema migrations,
// and CRUD for collections, links, chunks, and tags.
package storage

import (
	"context"
	"database/sql"
	"encoding/json"
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
		// Each handle gets its own isolated in-memory db. cache=shared used
		// to leak state across tests running in parallel and caused
		// "database is closed" races; per-handle isolation is what we want.
		return ":memory:?_journal_mode=DELETE&_foreign_keys=on"
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
	// Idempotent ADD COLUMNs for fields introduced after a deployment may
	// already exist. SQLite's ALTER TABLE ADD COLUMN errors out on a
	// duplicate, so we swallow that exact error and keep going.
	addColumns := map[string][]struct{ name, ddl string }{
		"links": {
			{"favicon_url", "TEXT"},
			{"extra_images", "TEXT"},
			{"order_idx", "REAL NOT NULL DEFAULT 0"},
			{"note", "TEXT"},
		},
		"collections": {
			{"feed_url", "TEXT"},
			{"last_checked_at", "INTEGER"},
			// View mode for the collection page. One of:
			// list (default) | grid | headlines | moodboard.
			{"layout", "TEXT NOT NULL DEFAULT 'list'"},
		},
	}
	for table, cols := range addColumns {
		for _, c := range cols {
			q := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, c.name, c.ddl)
			if _, err := s.db.ExecContext(ctx, q); err != nil {
				// "duplicate column name" is the harmless case.
				if !strings.Contains(err.Error(), "duplicate column") {
					return fmt.Errorf("alter %s.%s: %w", table, c.name, err)
				}
			}
		}
	}
	return nil
}

// ---- domain types ----

type Collection struct {
	ID            int64
	Slug          string
	Name          string
	Description   string
	FeedURL       string     // empty when this isn't a feed-backed collection
	LastCheckedAt *time.Time // last successful feed poll
	Layout        string     // list | grid | headlines | moodboard (default list)
	CreatedAt     time.Time
}

type Link struct {
	ID           int64
	CollectionID int64
	URL          string
	Title        string
	Description  string
	ImageURL     string   // primary preview image (og:image / twitter:image)
	FaviconURL   string   // site icon URL — never downloaded, just rendered
	ExtraImages  []string // additional images found on the page
	ContentMD    string
	ContentLang  string
	Summary      string
	Status       string
	ReadAt       *time.Time
	FetchError   string
	ArchivePath  string
	OrderIdx     float64
	Note         string // user-authored personal note (free text)
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
		`SELECT id, slug, name, COALESCE(description,''),
		        COALESCE(feed_url,''), last_checked_at, COALESCE(layout,'list'), created_at
		   FROM collections WHERE slug = ?`, slug)
	return scanCollection(row.Scan)
}

func scanCollection(scan func(...any) error) (*Collection, error) {
	var c Collection
	var ts int64
	var lastChecked sql.NullInt64
	err := scan(&c.ID, &c.Slug, &c.Name, &c.Description, &c.FeedURL, &lastChecked, &c.Layout, &ts)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scan collection: %w", err)
	}
	if lastChecked.Valid {
		t := time.Unix(lastChecked.Int64, 0).UTC()
		c.LastCheckedAt = &t
	}
	c.CreatedAt = time.Unix(ts, 0).UTC()
	return &c, nil
}

func (s *Store) ListCollections(ctx context.Context) ([]Collection, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, slug, name, COALESCE(description,''),
		        COALESCE(feed_url,''), last_checked_at, COALESCE(layout,'list'), created_at
		   FROM collections ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Collection
	for rows.Next() {
		c, err := scanCollection(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// CollectionStat is a Collection with link counts attached.
type CollectionStat struct {
	Collection
	Total      int // all links
	Summarized int // status=summarized — usable in RAG/chat
	InProgress int // pending or fetched (LLM hasn't finished)
	Failed     int // status=failed
}

// ListCollectionsWithStats is what the home page renders. One query,
// counts via CASE/WHEN per collection.
func (s *Store) ListCollectionsWithStats(ctx context.Context) ([]CollectionStat, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id, c.slug, c.name, COALESCE(c.description,''),
		       COALESCE(c.feed_url,''), c.last_checked_at, COALESCE(c.layout,'list'), c.created_at,
		       COUNT(l.id)                                                  AS total,
		       COUNT(CASE WHEN l.status = 'summarized' THEN 1 END)           AS summarized,
		       COUNT(CASE WHEN l.status = 'pending' THEN 1 END) AS in_progress,
		       COUNT(CASE WHEN l.status = 'failed' THEN 1 END)               AS failed
		  FROM collections c
		  LEFT JOIN links l ON l.collection_id = c.id
		 GROUP BY c.id
		 ORDER BY c.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CollectionStat
	for rows.Next() {
		var cs CollectionStat
		var ts int64
		var lastChecked sql.NullInt64
		if err := rows.Scan(&cs.ID, &cs.Slug, &cs.Name, &cs.Description,
			&cs.FeedURL, &lastChecked, &cs.Layout, &ts,
			&cs.Total, &cs.Summarized, &cs.InProgress, &cs.Failed); err != nil {
			return nil, err
		}
		if lastChecked.Valid {
			t := time.Unix(lastChecked.Int64, 0).UTC()
			cs.LastCheckedAt = &t
		}
		cs.CreatedAt = time.Unix(ts, 0).UTC()
		out = append(out, cs)
	}
	return out, rows.Err()
}

// CollectionStatsByID returns the same counts but for a single collection.
// Used by the per-collection page.
func (s *Store) CollectionStatsByID(ctx context.Context, id int64) (CollectionStat, error) {
	var cs CollectionStat
	var ts int64
	row := s.db.QueryRowContext(ctx, `
		SELECT c.id, c.slug, c.name, COALESCE(c.description,''),
		       COALESCE(c.feed_url,''), c.last_checked_at, COALESCE(c.layout,'list'), c.created_at,
		       COUNT(l.id),
		       COUNT(CASE WHEN l.status = 'summarized' THEN 1 END),
		       COUNT(CASE WHEN l.status = 'pending' THEN 1 END),
		       COUNT(CASE WHEN l.status = 'failed' THEN 1 END)
		  FROM collections c
		  LEFT JOIN links l ON l.collection_id = c.id
		 WHERE c.id = ?
		 GROUP BY c.id`, id)
	var lastChecked sql.NullInt64
	if err := row.Scan(&cs.ID, &cs.Slug, &cs.Name, &cs.Description,
		&cs.FeedURL, &lastChecked, &cs.Layout, &ts,
		&cs.Total, &cs.Summarized, &cs.InProgress, &cs.Failed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CollectionStat{}, ErrNotFound
		}
		return CollectionStat{}, err
	}
	if lastChecked.Valid {
		t := time.Unix(lastChecked.Int64, 0).UTC()
		cs.LastCheckedAt = &t
	}
	cs.CreatedAt = time.Unix(ts, 0).UTC()
	return cs, nil
}

// ---- preferences (theme, etc) ----

// GetPref returns the value for key, or "" + ErrNotFound when missing.
func (s *Store) GetPref(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx,
		`SELECT value FROM preferences WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return v, err
}

// SetPref upserts a preference row.
func (s *Store) SetPref(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO preferences(key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, time.Now().UTC().Unix())
	return err
}

// CountInProgress returns total links across all collections that are
// actively being worked on right now ("pending" — the worker still has
// to fetch + extract). Once a link reaches "fetched" it's a stable
// state until the user clicks "Generate summary"; counting it as
// "processing" is misleading because there's no active job.
func (s *Store) CountInProgress(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM links WHERE status = 'pending'`).Scan(&n)
	return n, err
}

// LinkStatusCounts returns global ready/in-progress/failed counters across
// all collections. Used by the chat page so the user sees how many links
// are actually retrievable before asking a question.
type LinkStatusCounts struct {
	Ready      int
	InProgress int
	Failed     int
}

func (s *Store) LinkStatusCounts(ctx context.Context) (LinkStatusCounts, error) {
	var c LinkStatusCounts
	err := s.db.QueryRowContext(ctx, `
		SELECT
			COUNT(CASE WHEN status = 'summarized' THEN 1 END),
			COUNT(CASE WHEN status IN ('pending','fetched') THEN 1 END),
			COUNT(CASE WHEN status = 'failed' THEN 1 END)
		FROM links`).Scan(&c.Ready, &c.InProgress, &c.Failed)
	return c, err
}

// DeleteCollection wipes the collection and every row that references
// it: link_tags → chunks → links → chat_messages → chat_sessions →
// collections. FK enforcement is off on this DB so we have to do the
// cascade manually inside a transaction.
func (s *Store) DeleteCollection(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	stmts := []string{
		`DELETE FROM link_tags WHERE link_id IN (SELECT id FROM links WHERE collection_id = ?)`,
		`DELETE FROM chunks    WHERE link_id IN (SELECT id FROM links WHERE collection_id = ?)`,
		`DELETE FROM links     WHERE collection_id = ?`,
		`DELETE FROM chat_messages WHERE session_id IN (SELECT id FROM chat_sessions WHERE collection_id = ?)`,
		`DELETE FROM chat_sessions WHERE collection_id = ?`,
		`DELETE FROM collections WHERE id = ?`,
	}
	for _, q := range stmts {
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			return fmt.Errorf("delete cascade: %w", err)
		}
	}
	return tx.Commit()
}

// RenameCollection updates the human-readable name and/or the slug.
// Empty slug or name → keep the existing one. Slug uniqueness is
// enforced by the schema; we surface the conflict as ErrSlugTaken so
// the UI can show a clean message instead of the raw SQL constraint
// error.
var ErrSlugTaken = errors.New("slug already taken")

func (s *Store) RenameCollection(ctx context.Context, id int64, newSlug, newName string) error {
	current, err := s.GetCollectionBySlugByID(ctx, id)
	if err != nil {
		return err
	}
	if newSlug == "" {
		newSlug = current.Slug
	}
	if newName == "" {
		newName = current.Name
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE collections SET slug = ?, name = ? WHERE id = ?`,
		newSlug, newName, id)
	if err != nil {
		// SQLite reports unique constraint failure with a recognisable
		// substring; map it to our typed error so the handler doesn't
		// have to substring-match.
		if strings.Contains(err.Error(), "UNIQUE") || strings.Contains(err.Error(), "unique") {
			return ErrSlugTaken
		}
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetCollectionBySlugByID looks up a collection by primary key.
func (s *Store) GetCollectionBySlugByID(ctx context.Context, id int64) (*Collection, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, slug, name, COALESCE(description,''),
		        COALESCE(feed_url,''), last_checked_at, COALESCE(layout,'list'), created_at
		   FROM collections WHERE id = ?`, id)
	return scanCollection(row.Scan)
}

// SetCollectionLayout updates the view mode for a collection.
// Accepts: list | grid | headlines | moodboard. Anything else returns
// an error before the SQL fires so we don't silently store junk.
func (s *Store) SetCollectionLayout(ctx context.Context, id int64, layout string) error {
	switch layout {
	case "list", "grid", "headlines", "moodboard":
	default:
		return fmt.Errorf("unsupported layout %q", layout)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE collections SET layout = ? WHERE id = ?`, layout, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetCollectionFeed assigns or clears the feed_url on a collection.
// Empty string clears it (the collection becomes a regular one again).
func (s *Store) SetCollectionFeed(ctx context.Context, id int64, feedURL string) error {
	feedURL = strings.TrimSpace(feedURL)
	_, err := s.db.ExecContext(ctx,
		`UPDATE collections SET feed_url = ? WHERE id = ?`, feedURL, id)
	return err
}

// MarkCollectionFeedChecked sets last_checked_at = now.
func (s *Store) MarkCollectionFeedChecked(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE collections SET last_checked_at = ? WHERE id = ?`,
		time.Now().UTC().Unix(), id)
	return err
}

// CreateLinkIfMissing inserts a link unless one with the same URL is
// already present in the collection. Returns the created (or existing)
// link plus a "created" flag. Used by the feed importer for dedupe.
func (s *Store) CreateLinkIfMissing(ctx context.Context, collectionID int64, urlStr string) (*Link, bool, error) {
	urlStr = strings.TrimSpace(urlStr)
	if urlStr == "" {
		return nil, false, fmt.Errorf("url required")
	}
	row := s.db.QueryRowContext(ctx,
		`SELECT id FROM links WHERE collection_id = ? AND url = ?`, collectionID, urlStr)
	var existingID int64
	if err := row.Scan(&existingID); err == nil {
		l, err := s.GetLink(ctx, existingID)
		return l, false, err
	}
	l, err := s.CreateLink(ctx, collectionID, urlStr)
	return l, err == nil, err
}

// ListFeedCollections returns every collection that has a non-empty
// feed_url. Used by the periodic poller.
func (s *Store) ListFeedCollections(ctx context.Context) ([]Collection, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, slug, name, COALESCE(description,''),
		        COALESCE(feed_url,''), last_checked_at, COALESCE(layout,'list'), created_at
		   FROM collections WHERE feed_url IS NOT NULL AND feed_url != ''
		   ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Collection
	for rows.Next() {
		c, err := scanCollection(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// ---- Links ----

func (s *Store) CreateLink(ctx context.Context, collectionID int64, urlStr string) (*Link, error) {
	urlStr = strings.TrimSpace(urlStr)
	if urlStr == "" {
		return nil, fmt.Errorf("url required")
	}
	now := time.Now().UTC()
	top, _ := s.maxOrderIdx(ctx, collectionID)
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO links(collection_id, url, status, order_idx, created_at) VALUES (?, ?, ?, ?, ?)`,
		collectionID, urlStr, StatusPending, top+1.0, now.Unix())
	if err != nil {
		return nil, fmt.Errorf("insert link: %w", err)
	}
	id, _ := res.LastInsertId()
	return &Link{ID: id, CollectionID: collectionID, URL: urlStr,
		Status: StatusPending, OrderIdx: top + 1.0, CreatedAt: now}, nil
}

func (s *Store) GetLink(ctx context.Context, id int64) (*Link, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, collection_id, url,
		        COALESCE(title,''), COALESCE(description,''), COALESCE(image_url,''),
		        COALESCE(favicon_url,''), COALESCE(extra_images,''),
		        COALESCE(content_md,''), COALESCE(content_lang,''), COALESCE(summary,''),
		        status, read_at, COALESCE(fetch_error,''), COALESCE(archive_path,''),
		        order_idx, COALESCE(note,''), fetched_at, created_at
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
		        COALESCE(favicon_url,''), COALESCE(extra_images,''),
		        COALESCE(content_md,''), COALESCE(content_lang,''), COALESCE(summary,''),
		        status, read_at, COALESCE(fetch_error,''), COALESCE(archive_path,''),
		        order_idx, COALESCE(note,''), fetched_at, created_at
		 FROM links WHERE collection_id = ?
		 ORDER BY order_idx DESC, created_at DESC LIMIT ? OFFSET ?`,
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
	var extraJSON string
	err := scan(&l.ID, &l.CollectionID, &l.URL,
		&l.Title, &l.Description, &l.ImageURL,
		&l.FaviconURL, &extraJSON,
		&l.ContentMD, &l.ContentLang, &l.Summary,
		&l.Status, &readAt, &l.FetchError, &l.ArchivePath,
		&l.OrderIdx, &l.Note, &fetchedAt, &createdAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if extraJSON != "" {
		// Tolerate legacy rows that may carry junk: a malformed list just
		// degrades gracefully to "no extra images" instead of poisoning
		// the read path.
		_ = json.Unmarshal([]byte(extraJSON), &l.ExtraImages)
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
	return s.UpdateLinkExtractionFull(ctx, id, title, desc, imageURL, "", nil, contentMD, lang, archivePath)
}

// UpdateLinkExtractionFull is the richer variant that also persists the
// favicon URL and extra images. Worker code uses this. Old call sites
// (mostly tests) still hit UpdateLinkExtraction which is a thin wrapper.
func (s *Store) UpdateLinkExtractionFull(ctx context.Context, id int64,
	title, desc, imageURL, faviconURL string, extraImages []string,
	contentMD, lang, archivePath string) error {

	var extraJSON string
	if len(extraImages) > 0 {
		b, err := json.Marshal(extraImages)
		if err != nil {
			return fmt.Errorf("marshal extra images: %w", err)
		}
		extraJSON = string(b)
	}
	now := time.Now().UTC().Unix()
	_, err := s.db.ExecContext(ctx,
		`UPDATE links
		   SET title = ?, description = ?, image_url = ?,
		       favicon_url = ?, extra_images = ?,
		       content_md = ?, content_lang = ?, archive_path = ?,
		       status = ?, fetched_at = ?, fetch_error = ''
		 WHERE id = ?`,
		title, desc, imageURL, faviconURL, extraJSON,
		contentMD, lang, archivePath,
		StatusFetched, now, id)
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

// UpdateLinkNote stores the user's free-form personal note. Empty
// string clears the note.
func (s *Store) UpdateLinkNote(ctx context.Context, id int64, note string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE links SET note = ? WHERE id = ?`, strings.TrimSpace(note), id)
	return err
}

// MarkLinkRead sets read_at to now (Inbox marks-as-read action).
func (s *Store) MarkLinkRead(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE links SET read_at = ? WHERE id = ?`, time.Now().UTC().Unix(), id)
	return err
}

// MarkLinkPending resets a link to pending so the worker re-fetches it.
// Used by the UI "refetch" button.
func (s *Store) MarkLinkPending(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE links SET status = ?, fetch_error = '' WHERE id = ?`, StatusPending, id)
	return err
}

// MarkLinkFetched resets a link to fetched so the worker re-runs the
// chunk/embed/summarize index pass without re-fetching upstream.
// Used by the UI "reindex" button.
func (s *Store) MarkLinkFetched(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE links SET status = ?, fetch_error = '' WHERE id = ?`, StatusFetched, id)
	return err
}

func (s *Store) DeleteLink(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM links WHERE id = ?`, id)
	return err
}

// MoveLink reassigns a link to a different collection. Refuses to move
// to an unknown collection (returns ErrNotFound). Order within the
// destination is the new top (max(order_idx)+1) so it lands on top.
func (s *Store) MoveLink(ctx context.Context, linkID, dstCollectionID int64) error {
	if _, err := s.GetCollectionBySlugByID(ctx, dstCollectionID); err != nil {
		return err
	}
	top, _ := s.maxOrderIdx(ctx, dstCollectionID)
	res, err := s.db.ExecContext(ctx,
		`UPDATE links SET collection_id = ?, order_idx = ? WHERE id = ?`,
		dstCollectionID, top+1.0, linkID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// maxOrderIdx returns the highest order_idx in a collection (0 when empty).
func (s *Store) maxOrderIdx(ctx context.Context, collectionID int64) (float64, error) {
	var v sql.NullFloat64
	err := s.db.QueryRowContext(ctx,
		`SELECT MAX(order_idx) FROM links WHERE collection_id = ?`,
		collectionID).Scan(&v)
	if err != nil {
		return 0, err
	}
	if !v.Valid {
		return 0, nil
	}
	return v.Float64, nil
}

// ReorderLink positions linkID inside its current (or new) collection
// either before or after pivotID. Sparse REAL ordering — we pick a
// midpoint between the pivot and its neighbour so we don't have to
// renumber the whole list. Cross-collection moves are supported by
// passing dstCollectionID > 0 (use 0 to keep the link in place).
//
// "after" = drop AFTER the pivot (visually below); "before" = drop
// BEFORE (visually above). With ORDER BY order_idx DESC, "above" has
// a HIGHER order_idx than the pivot.
func (s *Store) ReorderLink(ctx context.Context, linkID, pivotID, dstCollectionID int64, after bool) error {
	link, err := s.GetLink(ctx, linkID)
	if err != nil {
		return err
	}
	pivot, err := s.GetLink(ctx, pivotID)
	if err != nil {
		return err
	}
	colID := pivot.CollectionID
	if dstCollectionID > 0 {
		// Caller forced a destination; pivot must live there.
		if pivot.CollectionID != dstCollectionID {
			return fmt.Errorf("pivot %d not in collection %d", pivotID, dstCollectionID)
		}
		colID = dstCollectionID
	}

	// Find the neighbour on the side we're inserting on.
	// With ORDER BY order_idx DESC: "before" = neighbour with bigger
	// order_idx; "after" = neighbour with smaller order_idx.
	var neighbour sql.NullFloat64
	var q string
	if after {
		// Smallest order_idx STRICTLY LESS than pivot.
		q = `SELECT MAX(order_idx) FROM links
		      WHERE collection_id = ? AND order_idx < ? AND id != ?`
	} else {
		// Smallest order_idx STRICTLY GREATER than pivot.
		q = `SELECT MIN(order_idx) FROM links
		      WHERE collection_id = ? AND order_idx > ? AND id != ?`
	}
	if err := s.db.QueryRowContext(ctx, q, colID, pivot.OrderIdx, linkID).Scan(&neighbour); err != nil {
		return err
	}

	var newIdx float64
	switch {
	case after && !neighbour.Valid:
		// Pivot is the last → just go below it.
		newIdx = pivot.OrderIdx - 1.0
	case !after && !neighbour.Valid:
		// Pivot is the first → go above.
		newIdx = pivot.OrderIdx + 1.0
	case after:
		newIdx = (pivot.OrderIdx + neighbour.Float64) / 2.0
	default:
		newIdx = (pivot.OrderIdx + neighbour.Float64) / 2.0
	}

	_, err = s.db.ExecContext(ctx,
		`UPDATE links SET collection_id = ?, order_idx = ? WHERE id = ?`,
		colID, newIdx, linkID)
	if err != nil {
		return err
	}
	_ = link // keep the reference for error context if we ever expand it
	return nil
}

// ListLinksByStatus is the worker's inbox: oldest-first so older links don't
// starve when a burst of new pending rows arrives.
func (s *Store) ListLinksByStatus(ctx context.Context, status string, limit int) ([]Link, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, collection_id, url,
		        COALESCE(title,''), COALESCE(description,''), COALESCE(image_url,''),
		        COALESCE(favicon_url,''), COALESCE(extra_images,''),
		        COALESCE(content_md,''), COALESCE(content_lang,''), COALESCE(summary,''),
		        status, read_at, COALESCE(fetch_error,''), COALESCE(archive_path,''),
		        order_idx, COALESCE(note,''), fetched_at, created_at
		   FROM links WHERE status = ? ORDER BY created_at ASC LIMIT ?`,
		status, limit)
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

// ---- Chunks ----

// ReplaceChunks deletes any existing chunks for linkID and inserts the new
// ones in a single transaction. Used by reindex to avoid duplicate chunks
// piling up across runs.
func (s *Store) ReplaceChunks(ctx context.Context, linkID int64, texts []string) ([]int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM chunks WHERE link_id = ?`, linkID); err != nil {
		return nil, err
	}
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

// TagCount pairs a tag with its current usage count.
type TagCount struct {
	Tag
	Count int
}

// ListTagsWithCounts returns all tags ordered by usage desc. Used by /tags
// (cloud) and the per-collection sidebar tag filter.
func (s *Store) ListTagsWithCounts(ctx context.Context) ([]TagCount, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.id, t.slug, t.name, COUNT(lt.link_id) AS n
		  FROM tags t
		  LEFT JOIN link_tags lt ON lt.tag_id = t.id
		 GROUP BY t.id
		 ORDER BY n DESC, t.slug ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TagCount
	for rows.Next() {
		var tc TagCount
		if err := rows.Scan(&tc.ID, &tc.Slug, &tc.Name, &tc.Count); err != nil {
			return nil, err
		}
		out = append(out, tc)
	}
	return out, rows.Err()
}

// MergeTag re-attaches every link from src into dst (preserving source) and
// then deletes the now-orphan src tag. Idempotent: merging a tag into itself
// is a no-op.
func (s *Store) MergeTag(ctx context.Context, srcID, dstID int64) error {
	if srcID == dstID {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO link_tags(link_id, tag_id, source)
		SELECT link_id, ?, source FROM link_tags WHERE tag_id = ?`, dstID, srcID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM link_tags WHERE tag_id = ?`, srcID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM tags WHERE id = ?`, srcID); err != nil {
		return err
	}
	return tx.Commit()
}

// FindTagBySlug is a convenience for handlers that take slugs from URLs.
func (s *Store) FindTagBySlug(ctx context.Context, slug string) (*Tag, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, slug, name FROM tags WHERE slug = ?`, slug)
	var t Tag
	if err := row.Scan(&t.ID, &t.Slug, &t.Name); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &t, nil
}

// SearchLinksByTagPrefix returns link IDs whose tag slug or display name
// starts with q (case-insensitive). Used by the search engine so the
// global search bar finds links via their tags too, not just titles.
func (s *Store) SearchLinksByTagPrefix(ctx context.Context, q string, limit int) ([]int64, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}
	pat := strings.ToLower(q) + "%"
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT lt.link_id
		  FROM tags t
		  JOIN link_tags lt ON lt.tag_id = t.id
		 WHERE LOWER(t.slug) LIKE ? OR LOWER(t.name) LIKE ?
		 LIMIT ?`, pat, pat, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ListLinksByTag is used by the tag-cloud filter. Slug-keyed.
func (s *Store) ListLinksByTag(ctx context.Context, slug string, limit int) ([]Link, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT l.id, l.collection_id, l.url,
		       COALESCE(l.title,''), COALESCE(l.description,''), COALESCE(l.image_url,''),
		       COALESCE(l.favicon_url,''), COALESCE(l.extra_images,''),
		       COALESCE(l.content_md,''), COALESCE(l.content_lang,''), COALESCE(l.summary,''),
		       l.status, l.read_at, COALESCE(l.fetch_error,''), COALESCE(l.archive_path,''),
		       l.order_idx, COALESCE(l.note,''), l.fetched_at, l.created_at
		  FROM links l
		  JOIN link_tags lt ON lt.link_id = l.id
		  JOIN tags t       ON t.id = lt.tag_id
		 WHERE t.slug = ?
		 ORDER BY l.created_at DESC LIMIT ?`, slug, limit)
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

// ---- chat ----

type ChatMessage struct {
	ID        int64
	SessionID int64
	Role      string
	Content   string
	CreatedAt time.Time
}

// CreateChatSession returns the new session id. collectionID may be 0 for
// global (cross-collection) chats.
func (s *Store) CreateChatSession(ctx context.Context, collectionID int64) (int64, error) {
	now := time.Now().UTC().Unix()
	var res sql.Result
	var err error
	if collectionID > 0 {
		res, err = s.db.ExecContext(ctx,
			`INSERT INTO chat_sessions(collection_id, created_at) VALUES (?, ?)`, collectionID, now)
	} else {
		res, err = s.db.ExecContext(ctx,
			`INSERT INTO chat_sessions(created_at) VALUES (?)`, now)
	}
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	return id, nil
}

// AppendChatMessage stores a user/assistant/system message and returns its id.
func (s *Store) AppendChatMessage(ctx context.Context, sessionID int64, role, content string) (int64, error) {
	if role != "user" && role != "assistant" && role != "system" {
		return 0, fmt.Errorf("invalid role %q", role)
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO chat_messages(session_id, role, content, created_at) VALUES (?, ?, ?, ?)`,
		sessionID, role, content, time.Now().UTC().Unix())
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	return id, nil
}

// RecentChatMessages returns up to limit most-recent messages oldest-first
// (so callers can stitch them straight into a prompt).
func (s *Store) RecentChatMessages(ctx context.Context, sessionID int64, limit int) ([]ChatMessage, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, session_id, role, content, created_at
		  FROM chat_messages
		 WHERE session_id = ?
		 ORDER BY id DESC LIMIT ?`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var rev []ChatMessage
	for rows.Next() {
		var m ChatMessage
		var ts int64
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Role, &m.Content, &ts); err != nil {
			return nil, err
		}
		m.CreatedAt = time.Unix(ts, 0).UTC()
		rev = append(rev, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Reverse to oldest-first.
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev, nil
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
