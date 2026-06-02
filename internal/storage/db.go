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
	ChannelID   int64
	FeedURL     string
	FeedTitle   string
	GUID        string
	Title       string
	Link        string
	Description string
	Categories  string // JSON-массив или строка через запятую
	PublishedAt time.Time
	FetchedAt   time.Time
	Sent        bool
}

type Channel struct {
	ID             int64
	Name           string
	TelegramChatID string
	DigestCron     string
	Timezone       string
	Active         bool
}

type Feed struct {
	ID            int64
	ChannelID     int64
	URL           string
	Title         string
	Active        bool
	AddedAt       time.Time
	LastFetchedAt time.Time
	LastError     string
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
		CREATE TABLE IF NOT EXISTS channels (
			id               INTEGER PRIMARY KEY AUTOINCREMENT,
			name             TEXT NOT NULL UNIQUE,
			telegram_chat_id TEXT NOT NULL,
			digest_cron      TEXT NOT NULL,
			timezone         TEXT NOT NULL DEFAULT 'UTC',
			active           INTEGER NOT NULL DEFAULT 1
		);

		CREATE TABLE IF NOT EXISTS feeds (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			channel_id      INTEGER NOT NULL,
			url             TEXT NOT NULL,
			title           TEXT DEFAULT '',
			active          INTEGER NOT NULL DEFAULT 1,
			added_at        INTEGER NOT NULL,
			last_fetched_at INTEGER,
			last_error      TEXT,
			FOREIGN KEY (channel_id) REFERENCES channels(id) ON DELETE CASCADE
		);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_feeds_channel_url ON feeds(channel_id, url);

		CREATE TABLE IF NOT EXISTS articles (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			channel_id   INTEGER,
			feed_url     TEXT NOT NULL,
			feed_title   TEXT DEFAULT '',
			guid         TEXT NOT NULL,
			title        TEXT NOT NULL,
			link         TEXT NOT NULL,
			description  TEXT,
			categories   TEXT,
			published_at INTEGER,
			fetched_at   INTEGER NOT NULL,
			sent         INTEGER DEFAULT 0,
			UNIQUE(channel_id, guid),
			FOREIGN KEY (channel_id) REFERENCES channels(id) ON DELETE CASCADE
		);
		CREATE INDEX IF NOT EXISTS idx_articles_sent_fetched ON articles(sent, fetched_at);
		CREATE INDEX IF NOT EXISTS idx_articles_channel_sent ON articles(channel_id, sent);

		CREATE TABLE IF NOT EXISTS digests (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			channel_id INTEGER,
			content    TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			FOREIGN KEY (channel_id) REFERENCES channels(id) ON DELETE CASCADE
		);

		CREATE TABLE IF NOT EXISTS kv (
			key   TEXT PRIMARY KEY,
			value TEXT
		);
	`)
	if err != nil {
		return err
	}

	// Миграции для существующей базы: добавляем колонки только если их нет
	addColumnIfMissing := func(table, column, definition string) {
		var count int
		query := fmt.Sprintf("SELECT count(*) FROM pragma_table_info('%s') WHERE name='%s'", table, column)
		_ = s.db.QueryRow(query).Scan(&count)
		if count == 0 {
			_, _ = s.db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, definition))
		}
	}

	addColumnIfMissing("articles", "channel_id", "INTEGER")
	addColumnIfMissing("digests", "channel_id", "INTEGER")
	addColumnIfMissing("articles", "feed_title", "TEXT DEFAULT ''")
	addColumnIfMissing("articles", "categories", "TEXT")
	addColumnIfMissing("feeds", "added_at", "INTEGER NOT NULL DEFAULT 0")
	addColumnIfMissing("feeds", "active", "INTEGER NOT NULL DEFAULT 1")
	addColumnIfMissing("feeds", "last_fetched_at", "INTEGER")
	addColumnIfMissing("feeds", "last_error", "TEXT")

	return nil
}

func (s *DB) SaveDigest(ctx context.Context, channelID int64, content string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO digests (channel_id, content, created_at) VALUES (?, ?, ?)`,
		channelID, content, time.Now().Unix())
	return err
}

func (s *DB) GetLastDigest(ctx context.Context, channelID int64) (string, error) {
	var content string
	query := `SELECT content FROM digests WHERE channel_id = ? ORDER BY created_at DESC, id DESC LIMIT 1`
	err := s.db.QueryRowContext(ctx, query, channelID).Scan(&content)
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
		INSERT INTO articles (channel_id, feed_url, feed_title, guid, title, link, description, categories, published_at, fetched_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(channel_id, guid) DO UPDATE SET
			feed_title = excluded.feed_title,
			title = excluded.title,
			link = excluded.link,
			description = excluded.description,
			categories = excluded.categories,
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
		var channelID any = a.ChannelID
		if a.ChannelID == 0 {
			channelID = nil
		}
		_, err := stmt.ExecContext(ctx,
			channelID, a.FeedURL, a.FeedTitle, a.GUID, a.Title, a.Link, a.Description,
			a.Categories, pubAt, a.FetchedAt.Unix(),
		)
		if err != nil {
			return err
		}

		// Обновляем заголовок в таблице feeds, если он там пустой
		if a.FeedTitle != "" {
			_, _ = tx.ExecContext(ctx, `
				UPDATE feeds SET title = ? WHERE url = ? AND channel_id = ? AND (title IS NULL OR title = '')
			`, a.FeedTitle, a.FeedURL, a.ChannelID)
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

func (s *DB) GetUnsent(ctx context.Context, channelID int64, since time.Time) ([]Article, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT a.id, a.channel_id, a.feed_url, a.feed_title, a.guid, a.title, a.link, a.description, a.categories, a.published_at, a.fetched_at
		FROM articles a
		JOIN feeds f ON a.feed_url = f.url AND a.channel_id = f.channel_id
		WHERE a.sent = 0 AND a.channel_id = ? AND f.active = 1 AND COALESCE(a.published_at, a.fetched_at) >= ?
		ORDER BY COALESCE(a.published_at, a.fetched_at) ASC
	`, channelID, since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var articles []Article
	for rows.Next() {
		var a Article
		var pubAt *int64
		var fetchedAt int64
		var cID *int64
		var categories sql.NullString
		err := rows.Scan(&a.ID, &cID, &a.FeedURL, &a.FeedTitle, &a.GUID, &a.Title, &a.Link,
			&a.Description, &categories, &pubAt, &fetchedAt)
		if err != nil {
			return nil, err
		}
		a.Categories = categories.String
		if cID != nil {
			a.ChannelID = *cID
		}
		a.FetchedAt = time.Unix(fetchedAt, 0)
		if pubAt != nil {
			a.PublishedAt = time.Unix(*pubAt, 0)
		}
		articles = append(articles, a)
	}
	return articles, rows.Err()
}

func (s *DB) GetUnsentCount(ctx context.Context, channelID int64, since time.Time) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM articles a 
		 JOIN feeds f ON a.feed_url = f.url AND a.channel_id = f.channel_id
		 WHERE a.sent = 0 AND a.channel_id = ? AND f.active = 1 AND COALESCE(a.published_at, a.fetched_at) >= ?`,
		channelID, since.Unix(),
	).Scan(&count)
	return count, err
}

func (s *DB) GetLatestPerFeed(ctx context.Context, channelID int64, limit int) ([]Article, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, channel_id, feed_url, feed_title, guid, title, link, description, categories, published_at, fetched_at, sent
		FROM (
			SELECT
				a.id, a.channel_id, a.feed_url, a.feed_title, a.guid, a.title, a.link, a.description, a.categories, a.published_at, a.fetched_at, a.sent,
				ROW_NUMBER() OVER (
					PARTITION BY a.feed_url
					ORDER BY COALESCE(a.published_at, a.fetched_at) DESC
				) AS rn
			FROM articles a
			JOIN feeds f ON a.feed_url = f.url AND a.channel_id = f.channel_id
			WHERE a.channel_id = ? AND f.active = 1
		)
		WHERE rn <= ?
		ORDER BY feed_url ASC, COALESCE(published_at, fetched_at) DESC
	`, channelID, limit)
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
		var cID *int64
		var categories sql.NullString
		err := rows.Scan(&a.ID, &cID, &a.FeedURL, &a.FeedTitle, &a.GUID, &a.Title, &a.Link,
			&a.Description, &categories, &pubAt, &fetchedAt, &sent)
		if err != nil {
			return nil, err
		}
		a.Categories = categories.String
		if cID != nil {
			a.ChannelID = *cID
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

func (s *DB) GetStats(ctx context.Context, channelID int64) (Stats, error) {
	var st Stats
	var lastFetch *int64
	err := s.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			COALESCE(SUM(sent), 0),
			MAX(fetched_at)
		FROM articles
		WHERE channel_id = ?
	`, channelID).Scan(&st.TotalArticles, &st.SentArticles, &lastFetch)
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

func (s *DB) GetDefaultChannel(ctx context.Context) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM channels LIMIT 1`).Scan(&id)
	if err == sql.ErrNoRows {
		return s.UpsertChannel(ctx, Channel{Name: "Default", TelegramChatID: "0", DigestCron: "0 9 * * *", Active: true})
	}
	return id, err
}

func (s *DB) AddFeed(ctx context.Context, url string) error {
	chID, err := s.GetDefaultChannel(ctx)
	if err != nil {
		return err
	}
	return s.UpsertFeed(ctx, Feed{ChannelID: chID, URL: url, Active: true})
}

func (s *DB) GetAllFeeds(ctx context.Context) ([]Feed, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, channel_id, url, title, active, added_at, last_fetched_at, last_error 
		FROM feeds ORDER BY added_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var feeds []Feed
	for rows.Next() {
		var f Feed
		var active int
		var addedAt int64
		var lastFetched *int64
		var lastErr sql.NullString
		if err := rows.Scan(&f.ID, &f.ChannelID, &f.URL, &f.Title, &active, &addedAt, &lastFetched, &lastErr); err != nil {
			return nil, err
		}
		f.Active = active != 0
		f.AddedAt = time.Unix(addedAt, 0)
		if lastFetched != nil {
			f.LastFetchedAt = time.Unix(*lastFetched, 0)
		}
		f.LastError = lastErr.String
		feeds = append(feeds, f)
	}
	return feeds, nil
}

func (s *DB) SyncFeeds(ctx context.Context, envURLs []string) error {
	chID, err := s.GetDefaultChannel(ctx)
	if err != nil {
		return err
	}
	for _, url := range envURLs {
		_ = s.UpsertFeed(ctx, Feed{ChannelID: chID, URL: url, Active: true})
	}
	return nil
}

func (s *DB) GetActiveFeedURLs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT url FROM feeds WHERE active = 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var urls []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		urls = append(urls, u)
	}
	return urls, nil
}

func (s *DB) ToggleFeed(ctx context.Context, url string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE feeds SET active = 1 - active WHERE url = ?`, url)
	return err
}

func (s *DB) UpdateFeedTitle(ctx context.Context, url, title string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE feeds SET title = ? WHERE url = ?`, title, url)
	return err
}

func (s *DB) UpdateFeedTitleByID(ctx context.Context, id int64, title string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE feeds SET title = ? WHERE id = ?`, title, id)
	return err
}

func (s *DB) UpdateFeedStatus(ctx context.Context, url string, err error) error {
	now := time.Now().Unix()
	var errMsg string
	if err != nil {
		errMsg = err.Error()
	}
	_, dbErr := s.db.ExecContext(ctx, `
		UPDATE feeds SET last_fetched_at = ?, last_error = ? WHERE url = ?
	`, now, errMsg, url)
	return dbErr
}

func (s *DB) SetState(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO kv (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value
	`, key, value)
	return err
}

func (s *DB) GetState(ctx context.Context, key string) (string, error) {
	var val string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM kv WHERE key = ?`, key).Scan(&val)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return val, err
}

func (s *DB) GetChannels(ctx context.Context) ([]Channel, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, telegram_chat_id, digest_cron, timezone, active FROM channels`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var channels []Channel
	for rows.Next() {
		var c Channel
		var active int
		if err := rows.Scan(&c.ID, &c.Name, &c.TelegramChatID, &c.DigestCron, &c.Timezone, &active); err != nil {
			return nil, err
		}
		c.Active = active != 0
		channels = append(channels, c)
	}
	return channels, nil
}

func (s *DB) GetFeeds(ctx context.Context, channelID int64) ([]Feed, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, channel_id, url, title, active, added_at, last_fetched_at, last_error 
		FROM feeds WHERE channel_id = ?`, channelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var feeds []Feed
	for rows.Next() {
		var f Feed
		var active int
		var addedAt int64
		var lastFetched *int64
		var lastErr sql.NullString
		if err := rows.Scan(&f.ID, &f.ChannelID, &f.URL, &f.Title, &active, &addedAt, &lastFetched, &lastErr); err != nil {
			return nil, err
		}
		f.Active = active != 0
		f.AddedAt = time.Unix(addedAt, 0)
		if lastFetched != nil {
			f.LastFetchedAt = time.Unix(*lastFetched, 0)
		}
		f.LastError = lastErr.String
		feeds = append(feeds, f)
	}
	return feeds, nil
}

func (s *DB) UpsertChannel(ctx context.Context, c Channel) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO channels (name, telegram_chat_id, digest_cron, timezone, active)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			telegram_chat_id = excluded.telegram_chat_id,
			digest_cron = excluded.digest_cron,
			timezone = excluded.timezone,
			active = excluded.active
	`, c.Name, c.TelegramChatID, c.DigestCron, c.Timezone, c.Active)
	if err != nil {
		return 0, err
	}
	if c.ID != 0 {
		return c.ID, nil
	}
	return res.LastInsertId()
}

func (s *DB) UpsertFeed(ctx context.Context, f Feed) error {
	var err error
	if f.ID == 0 {
		_, err = s.db.ExecContext(ctx, `
			INSERT INTO feeds (channel_id, url, title, active, added_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(channel_id, url) DO UPDATE SET
				title = CASE WHEN excluded.title != '' THEN excluded.title ELSE feeds.title END,
				active = excluded.active
		`, f.ChannelID, f.URL, f.Title, f.Active, time.Now().Unix())
	} else {
		_, err = s.db.ExecContext(ctx, `
			UPDATE feeds SET url = ?, title = ?, active = ? WHERE id = ?
		`, f.URL, f.Title, f.Active, f.ID)
	}
	return err
}

func (s *DB) GetFeedByID(ctx context.Context, id int64) (Feed, error) {
	var f Feed
	var active int
	var addedAt int64
	var lastFetched *int64
	var lastErr sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT id, channel_id, url, title, active, added_at, last_fetched_at, last_error 
		FROM feeds WHERE id = ?`, id).Scan(&f.ID, &f.ChannelID, &f.URL, &f.Title, &active, &addedAt, &lastFetched, &lastErr)
	if err != nil {
		return f, err
	}
	f.Active = active != 0
	f.AddedAt = time.Unix(addedAt, 0)
	if lastFetched != nil {
		f.LastFetchedAt = time.Unix(*lastFetched, 0)
	}
	f.LastError = lastErr.String
	return f, nil
}

func (s *DB) DeleteChannel(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM channels WHERE id = ?`, id)
	return err
}

func (s *DB) DeleteFeed(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM feeds WHERE id = ?`, id)
	return err
}

func (s *DB) UpdateChannelName(ctx context.Context, id int64, name string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE channels SET name = ? WHERE id = ?`, name, id)
	return err
}

func (s *DB) UpdateChannelCron(ctx context.Context, id int64, cron string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE channels SET digest_cron = ? WHERE id = ?`, cron, id)
	return err
}

