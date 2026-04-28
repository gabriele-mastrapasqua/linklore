package storage

// migrations is the canonical schema. Every statement is idempotent
// (`CREATE ... IF NOT EXISTS`) so running them at every startup is safe.
// Mirrors graphrag's inline migration pattern in storage/sqlite.go.
var migrations = []string{
	`CREATE TABLE IF NOT EXISTS collections (
		id          INTEGER PRIMARY KEY,
		slug        TEXT NOT NULL UNIQUE,
		name        TEXT NOT NULL,
		description TEXT,
		created_at  INTEGER NOT NULL
	)`,

	`CREATE TABLE IF NOT EXISTS links (
		id            INTEGER PRIMARY KEY,
		collection_id INTEGER NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
		url           TEXT NOT NULL,
		title         TEXT,
		description   TEXT,
		image_url     TEXT,
		content_md    TEXT,
		content_lang  TEXT,
		summary       TEXT,
		status        TEXT NOT NULL,
		read_at       INTEGER,
		fetch_error   TEXT,
		archive_path  TEXT,
		fetched_at    INTEGER,
		created_at    INTEGER NOT NULL,
		UNIQUE(collection_id, url)
	)`,

	`CREATE TABLE IF NOT EXISTS chunks (
		id        INTEGER PRIMARY KEY,
		link_id   INTEGER NOT NULL REFERENCES links(id) ON DELETE CASCADE,
		ord       INTEGER NOT NULL,
		text      TEXT NOT NULL,
		embedding BLOB
	)`,

	`CREATE TABLE IF NOT EXISTS tags (
		id   INTEGER PRIMARY KEY,
		slug TEXT NOT NULL UNIQUE,
		name TEXT NOT NULL
	)`,

	`CREATE TABLE IF NOT EXISTS link_tags (
		link_id INTEGER NOT NULL REFERENCES links(id) ON DELETE CASCADE,
		tag_id  INTEGER NOT NULL REFERENCES tags(id)  ON DELETE CASCADE,
		source  TEXT NOT NULL,
		PRIMARY KEY (link_id, tag_id)
	)`,

	`CREATE TABLE IF NOT EXISTS chat_sessions (
		id            INTEGER PRIMARY KEY,
		collection_id INTEGER REFERENCES collections(id) ON DELETE SET NULL,
		created_at    INTEGER NOT NULL
	)`,

	`CREATE TABLE IF NOT EXISTS chat_messages (
		id          INTEGER PRIMARY KEY,
		session_id  INTEGER NOT NULL REFERENCES chat_sessions(id) ON DELETE CASCADE,
		role        TEXT NOT NULL,
		content     TEXT NOT NULL,
		created_at  INTEGER NOT NULL
	)`,

	`CREATE INDEX IF NOT EXISTS idx_links_collection ON links(collection_id)`,
	`CREATE INDEX IF NOT EXISTS idx_links_status     ON links(status)`,
	`CREATE INDEX IF NOT EXISTS idx_links_read_at    ON links(read_at)`,
	`CREATE INDEX IF NOT EXISTS idx_chunks_link      ON chunks(link_id, ord)`,
	`CREATE INDEX IF NOT EXISTS idx_chat_msgs_session ON chat_messages(session_id, id)`,

	`CREATE VIRTUAL TABLE IF NOT EXISTS links_fts USING fts5(
		title, description, summary, content_md,
		content='links', content_rowid='id'
	)`,
	`CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(
		text, content='chunks', content_rowid='id'
	)`,

	// Triggers keep FTS shadow tables in sync with the base tables.
	`CREATE TRIGGER IF NOT EXISTS links_ai AFTER INSERT ON links BEGIN
		INSERT INTO links_fts(rowid, title, description, summary, content_md)
		VALUES (new.id, new.title, new.description, new.summary, new.content_md);
	END`,
	`CREATE TRIGGER IF NOT EXISTS links_ad AFTER DELETE ON links BEGIN
		INSERT INTO links_fts(links_fts, rowid, title, description, summary, content_md)
		VALUES('delete', old.id, old.title, old.description, old.summary, old.content_md);
	END`,
	`CREATE TRIGGER IF NOT EXISTS links_au AFTER UPDATE ON links BEGIN
		INSERT INTO links_fts(links_fts, rowid, title, description, summary, content_md)
		VALUES('delete', old.id, old.title, old.description, old.summary, old.content_md);
		INSERT INTO links_fts(rowid, title, description, summary, content_md)
		VALUES (new.id, new.title, new.description, new.summary, new.content_md);
	END`,

	`CREATE TRIGGER IF NOT EXISTS chunks_ai AFTER INSERT ON chunks BEGIN
		INSERT INTO chunks_fts(rowid, text) VALUES (new.id, new.text);
	END`,
	`CREATE TRIGGER IF NOT EXISTS chunks_ad AFTER DELETE ON chunks BEGIN
		INSERT INTO chunks_fts(chunks_fts, rowid, text) VALUES('delete', old.id, old.text);
	END`,
	`CREATE TRIGGER IF NOT EXISTS chunks_au AFTER UPDATE ON chunks BEGIN
		INSERT INTO chunks_fts(chunks_fts, rowid, text) VALUES('delete', old.id, old.text);
		INSERT INTO chunks_fts(rowid, text) VALUES (new.id, new.text);
	END`,
}
