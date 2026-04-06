package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

type Article struct {
	ID          int64
	FeedURL     string
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
			guid         TEXT NOT NULL UNIQUE,
			title        TEXT NOT NULL,
			link         TEXT NOT NULL,
			description  TEXT,
			published_at INTEGER,
			fetched_at   INTEGER NOT NULL,
			sent         INTEGER DEFAULT 0
		);
		CREATE INDEX IF NOT EXISTS idx_articles_sent_fetched ON articles(sent, fetched_at);
	`)
	return err
}

func (s *DB) SaveArticles(ctx context.Context, articles []Article) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT OR IGNORE INTO articles (feed_url, guid, title, link, description, published_at, fetched_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
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
			a.FeedURL, a.GUID, a.Title, a.Link, a.Description,
			pubAt, a.FetchedAt.Unix(),
		)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *DB) GetUnsent(ctx context.Context, since time.Time) ([]Article, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, feed_url, guid, title, link, description, published_at, fetched_at
		FROM articles
		WHERE sent = 0 AND fetched_at >= ?
		ORDER BY fetched_at ASC
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
		err := rows.Scan(&a.ID, &a.FeedURL, &a.GUID, &a.Title, &a.Link,
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

func (s *DB) MarkSent(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `UPDATE articles SET sent = 1 WHERE id = ?`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, id := range ids {
		if _, err := stmt.ExecContext(ctx, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
