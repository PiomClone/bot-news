package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-go sqlite driver
)

type Article struct {
	ID          int64
	FeedURL     string
	FeedTitle   string
	GUID        string
	Title       string
	Link        string
	Description string
	PublishedAt time.Time
	FetchedAt   time.Time
	Sent        bool
}

type DB struct {
	db *sql.DB
}

func NewDB(path string) (*DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	store := &DB{db: db}
	if err := store.applyPragmas(); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite pragmas: %w", err)
	}
	if err := store.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *DB) Close() error {
	return s.db.Close()
}

func (s *DB) applyPragmas() error {
	_, err := s.db.Exec(`
		PRAGMA journal_mode=WAL;
		PRAGMA busy_timeout=5000;
		PRAGMA synchronous=NORMAL;
	`)
	return err
}

func (s *DB) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS articles (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			feed_url     TEXT NOT NULL,
			feed_title   TEXT DEFAULT '',
			guid         TEXT NOT NULL UNIQUE,
			title        TEXT NOT NULL,
			link         TEXT NOT NULL,
			description  TEXT,
			published_at INTEGER,
			fetched_at   INTEGER NOT NULL,
			sent         INTEGER DEFAULT 0
		);
		CREATE INDEX IF NOT EXISTS idx_articles_sent_fetched ON articles(sent, fetched_at);

		CREATE TABLE IF NOT EXISTS digests (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			content    TEXT NOT NULL,
			created_at INTEGER NOT NULL
		);
	`)
	if err != nil {
		return err
	}

	// Миграция для существующей базы: добавляем колонку, если её нет
	_, _ = s.db.Exec("ALTER TABLE articles ADD COLUMN feed_title TEXT DEFAULT ''")

	return nil
}

func (s *DB) SaveDigest(ctx context.Context, content string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO digests (content, created_at) VALUES (?, ?)`,
		content, time.Now().Unix())
	return err
}

func (s *DB) GetLastDigest(ctx context.Context) (string, error) {
	var content string
	query := `SELECT content FROM digests ORDER BY created_at DESC, id DESC LIMIT 1`
	err := s.db.QueryRowContext(ctx, query).Scan(&content)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return content, err
}

func (s *DB) SaveArticles(ctx context.Context, articles []Article) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO articles (feed_url, feed_title, guid, title, link, description, published_at, fetched_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(guid) DO UPDATE SET
			feed_title = excluded.feed_title,
			title = excluded.title,
			link = excluded.link,
			description = excluded.description,
			published_at = excluded.published_at
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, a := range articles {
		var pubAt *int64
		if !a.PublishedAt.IsZero() {
			v := a.PublishedAt.Unix()
			pubAt = &v
		}
		_, err := stmt.ExecContext(ctx,
			a.FeedURL, a.FeedTitle, a.GUID, a.Title, a.Link, a.Description,
			pubAt, a.FetchedAt.Unix(),
		)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *DB) DeleteOldArticles(ctx context.Context, days int) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM articles 
		WHERE fetched_at < ? AND sent = 1
	`, time.Now().AddDate(0, 0, -days).Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *DB) GetUnsent(ctx context.Context, since time.Time) ([]Article, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, feed_url, feed_title, guid, title, link, description, published_at, fetched_at
		FROM articles
		WHERE sent = 0 AND COALESCE(published_at, fetched_at) >= ?
		ORDER BY COALESCE(published_at, fetched_at) ASC
	`, since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var articles []Article
	for rows.Next() {
		var a Article
		var pubAt *int64
		var fetchedAt int64
		err := rows.Scan(&a.ID, &a.FeedURL, &a.FeedTitle, &a.GUID, &a.Title, &a.Link,
			&a.Description, &pubAt, &fetchedAt)
		if err != nil {
			return nil, err
		}
		a.FetchedAt = time.Unix(fetchedAt, 0)
		if pubAt != nil {
			a.PublishedAt = time.Unix(*pubAt, 0)
		}
		articles = append(articles, a)
	}
	return articles, rows.Err()
}

func (s *DB) GetUnsentCount(ctx context.Context, since time.Time) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM articles WHERE sent = 0 AND COALESCE(published_at, fetched_at) >= ?`,
		since.Unix(),
	).Scan(&count)
	return count, err
}

func (s *DB) GetLatestPerFeed(ctx context.Context, limit int) ([]Article, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, feed_url, feed_title, guid, title, link, description, published_at, fetched_at, sent
		FROM (
			SELECT
				id, feed_url, feed_title, guid, title, link, description, published_at, fetched_at, sent,
				ROW_NUMBER() OVER (
					PARTITION BY feed_url
					ORDER BY COALESCE(published_at, fetched_at) DESC
				) AS rn
			FROM articles
		)
		WHERE rn <= ?
		ORDER BY feed_url ASC, COALESCE(published_at, fetched_at) DESC
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var articles []Article
	for rows.Next() {
		var a Article
		var pubAt *int64
		var fetchedAt int64
		var sent int
		err := rows.Scan(&a.ID, &a.FeedURL, &a.FeedTitle, &a.GUID, &a.Title, &a.Link,
			&a.Description, &pubAt, &fetchedAt, &sent)
		if err != nil {
			return nil, err
		}
		a.FetchedAt = time.Unix(fetchedAt, 0)
		if pubAt != nil {
			a.PublishedAt = time.Unix(*pubAt, 0)
		}
		a.Sent = sent != 0
		articles = append(articles, a)
	}
	return articles, rows.Err()
}

type Stats struct {
	TotalArticles int64
	SentArticles  int64
	LastFetchedAt time.Time
}

func (s *DB) GetStats(ctx context.Context) (Stats, error) {
	var st Stats
	var lastFetch *int64
	err := s.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			COALESCE(SUM(sent), 0),
			MAX(fetched_at)
		FROM articles
	`).Scan(&st.TotalArticles, &st.SentArticles, &lastFetch)
	if err != nil {
		return st, err
	}
	if lastFetch != nil {
		st.LastFetchedAt = time.Unix(*lastFetch, 0)
	}
	return st, nil
}

func (s *DB) MarkSent(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	// nolint:gosec // G202: SQL string concatenation. Placeholders are safe here.
	query := "UPDATE articles SET sent = 1 WHERE id IN (" + strings.Join(placeholders, ",") + ")"
	_, err := s.db.ExecContext(ctx, query, args...)
	return err
}
