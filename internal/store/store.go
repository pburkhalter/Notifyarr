// Package store keeps the small bit of state the bot can't recover from
// upstream APIs: which numeric reply maps to which TMDB id (active
// searches), who got welcomed when (welcome dedup), and which poll-option
// maps to which media (active polls). Everything is TTL'd; the schema is
// intentionally simple.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Store is the bot's tiny SQLite-backed state.
type Store struct{ db *sql.DB }

// ErrNotFound is returned by lookups that don't match. The bot treats it
// as "no recent context" rather than an error to log.
var ErrNotFound = errors.New("not found")

// Open opens (and creates if absent) the sqlite DB at path and applies
// migrations. WAL mode keeps reader/writer contention low.
func Open(ctx context.Context, path string) (*Store, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(ON)")
	dsn := "file:" + abs + "?" + q.Encode()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS searches (
  chat_id     TEXT NOT NULL,
  sender_id   TEXT NOT NULL,
  slot        INTEGER NOT NULL,  -- 1, 2, 3 from the suche reply
  tmdb_id     INTEGER NOT NULL,
  media_type  TEXT NOT NULL,     -- "movie" | "tv"
  title       TEXT NOT NULL,
  expires_at  DATETIME NOT NULL,
  PRIMARY KEY (chat_id, sender_id, slot)
);
CREATE INDEX IF NOT EXISTS idx_searches_exp ON searches(expires_at);

CREATE TABLE IF NOT EXISTS welcomes (
  chat_id    TEXT NOT NULL,
  user_id    TEXT NOT NULL,
  welcomed_at DATETIME NOT NULL,
  PRIMARY KEY (chat_id, user_id)
);

CREATE TABLE IF NOT EXISTS polls (
  poll_id     TEXT NOT NULL,
  option_idx  INTEGER NOT NULL,
  tmdb_id     INTEGER NOT NULL,
  media_type  TEXT NOT NULL,
  title       TEXT NOT NULL,
  PRIMARY KEY (poll_id, option_idx)
);

-- Pending notification buffer. The bot batches Sonarr "episode imported"
-- events: when >=3 episodes of the same series arrive within 10 min, the
-- bot flushes a single grouped message instead of N pings.
CREATE TABLE IF NOT EXISTS pending_imports (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  show_key     TEXT NOT NULL,    -- "<series_id>:<season>" or "<movie_tmdb>" for movies
  display_name TEXT NOT NULL,    -- human-readable, e.g. "Family Guy S20"
  added_at     DATETIME NOT NULL,
  flushed_at   DATETIME,
  payload      TEXT NOT NULL     -- json blob with episode title + s/e + tmdb id
);
CREATE INDEX IF NOT EXISTS idx_pending_show ON pending_imports(show_key) WHERE flushed_at IS NULL;
`
	_, err := s.db.ExecContext(ctx, schema)
	return err
}

// ─── searches ────────────────────────────────────────────────────────────

// SearchResult is one row in a recorded search.
type SearchResult struct {
	Slot      int
	TMDBID    int
	MediaType string
	Title     string
}

// SaveSearch replaces any previous search for this (chat, sender) with the
// new results. The bot keeps only the latest search so numeric replies are
// unambiguous.
func (s *Store) SaveSearch(ctx context.Context, chatID, senderID string, results []SearchResult, ttl time.Duration) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM searches WHERE chat_id = ? AND sender_id = ?`, chatID, senderID); err != nil {
		return err
	}
	expires := time.Now().UTC().Add(ttl)
	for _, r := range results {
		if _, err := tx.ExecContext(ctx, `INSERT INTO searches
			(chat_id, sender_id, slot, tmdb_id, media_type, title, expires_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			chatID, senderID, r.Slot, r.TMDBID, r.MediaType, r.Title, expires); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// LookupSearch returns the SearchResult the sender picked, if their last
// search is still within TTL. Returns ErrNotFound otherwise.
func (s *Store) LookupSearch(ctx context.Context, chatID, senderID string, slot int) (*SearchResult, error) {
	row := s.db.QueryRowContext(ctx, `SELECT slot, tmdb_id, media_type, title FROM searches
		WHERE chat_id = ? AND sender_id = ? AND slot = ? AND expires_at > CURRENT_TIMESTAMP`,
		chatID, senderID, slot)
	r := &SearchResult{}
	if err := row.Scan(&r.Slot, &r.TMDBID, &r.MediaType, &r.Title); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return r, nil
}

// ReapSearches deletes expired search rows. Call from a periodic ticker.
func (s *Store) ReapSearches(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM searches WHERE expires_at <= CURRENT_TIMESTAMP`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ─── welcomes ────────────────────────────────────────────────────────────

// MarkWelcomed records a welcome event. Returns true when this is the
// first welcome for (chat, user) within `cooldown`; false when we've
// already greeted them recently and should suppress the new greeting.
func (s *Store) MarkWelcomed(ctx context.Context, chatID, userID string, cooldown time.Duration) (bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT welcomed_at FROM welcomes WHERE chat_id = ? AND user_id = ?`, chatID, userID)
	var prev time.Time
	switch err := row.Scan(&prev); {
	case errors.Is(err, sql.ErrNoRows):
		// fall through and insert
	case err != nil:
		return false, err
	default:
		if time.Since(prev) < cooldown {
			return false, nil
		}
	}
	_, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO welcomes (chat_id, user_id, welcomed_at)
		VALUES (?, ?, CURRENT_TIMESTAMP)`, chatID, userID)
	return err == nil, err
}

// ─── polls ───────────────────────────────────────────────────────────────

// PollOption is a single answer the bot offered.
type PollOption struct {
	Index     int
	TMDBID    int
	MediaType string
	Title     string
}

// SavePoll records the option → media mapping for a poll the bot just sent.
// Used by the poll-vote webhook handler to know what was voted for.
func (s *Store) SavePoll(ctx context.Context, pollID string, opts []PollOption) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, o := range opts {
		if _, err := tx.ExecContext(ctx, `INSERT INTO polls
			(poll_id, option_idx, tmdb_id, media_type, title) VALUES (?, ?, ?, ?, ?)`,
			pollID, o.Index, o.TMDBID, o.MediaType, o.Title); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// LookupPoll returns the option a voter picked. ErrNotFound when the
// pollID isn't known to the bot (very old, or someone else's poll).
func (s *Store) LookupPoll(ctx context.Context, pollID string, idx int) (*PollOption, error) {
	row := s.db.QueryRowContext(ctx, `SELECT option_idx, tmdb_id, media_type, title FROM polls
		WHERE poll_id = ? AND option_idx = ?`, pollID, idx)
	o := &PollOption{}
	if err := row.Scan(&o.Index, &o.TMDBID, &o.MediaType, &o.Title); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return o, nil
}

// ─── pending imports ─────────────────────────────────────────────────────

// EnqueuePendingImport buffers a single episode/movie notification for
// grouped flushing. PayloadJSON is opaque to the store (the handler
// serializes whatever fields it'll need at flush time).
func (s *Store) EnqueuePendingImport(ctx context.Context, showKey, displayName, payloadJSON string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO pending_imports
		(show_key, display_name, added_at, payload) VALUES (?, ?, CURRENT_TIMESTAMP, ?)`,
		showKey, displayName, payloadJSON)
	return err
}

// PendingImport is one buffered row.
type PendingImport struct {
	ID          int64
	ShowKey     string
	DisplayName string
	AddedAt     time.Time
	PayloadJSON string
}

// DueImports returns ALL pending imports for any show that has settled:
//   - oldest pending row for the show is older than `wait` (long enough that we
//     should ship something), AND
//   - newest pending row for the show is older than `quietPeriod` (no fresh
//     activity, so the season import has probably finished trickling in).
//
// The two-condition gate keeps a 22-episode Sonarr import — which lands one
// row every few minutes — out of N individual messages; we wait until the
// whole burst is in, then flush them as a single group. Caller flushes each
// group as one message and then calls MarkFlushed.
func (s *Store) DueImports(ctx context.Context, wait, quietPeriod time.Duration) (map[string][]PendingImport, error) {
	matureCutoff := time.Now().UTC().Add(-wait)
	quietCutoff := time.Now().UTC().Add(-quietPeriod)
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, show_key, display_name, added_at, payload
		FROM pending_imports
		WHERE flushed_at IS NULL
		  AND show_key IN (
		      SELECT show_key FROM pending_imports
		      WHERE flushed_at IS NULL
		      GROUP BY show_key
		      HAVING MIN(added_at) <= ? AND MAX(added_at) <= ?
		  )
		ORDER BY show_key, added_at ASC`, matureCutoff, quietCutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]PendingImport{}
	for rows.Next() {
		p := PendingImport{}
		if err := rows.Scan(&p.ID, &p.ShowKey, &p.DisplayName, &p.AddedAt, &p.PayloadJSON); err != nil {
			return nil, err
		}
		out[p.ShowKey] = append(out[p.ShowKey], p)
	}
	return out, rows.Err()
}

// MarkFlushed clears a batch of pending imports after the bot posted the
// grouped message.
func (s *Store) MarkFlushed(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx,
			`UPDATE pending_imports SET flushed_at = CURRENT_TIMESTAMP WHERE id = ?`, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
