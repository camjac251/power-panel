package db

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

type PowerEvent struct {
	ID         int64
	Timestamp  time.Time
	Action     string
	UserLogin  string
	UserName   string
	Success    bool
	ErrorMsg   string
	DurationMS int64
}

const schema = `
CREATE TABLE IF NOT EXISTS power_events (
    id          INTEGER PRIMARY KEY,
    timestamp   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    action      TEXT    NOT NULL,
    user_login  TEXT    NOT NULL,
    user_name   TEXT    NOT NULL DEFAULT '',
    success     INTEGER NOT NULL DEFAULT 1,
    error_msg   TEXT,
    duration_ms INTEGER
) STRICT;

CREATE INDEX IF NOT EXISTS idx_power_events_timestamp
    ON power_events(timestamp DESC);
`

func Open(ctx context.Context, dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return nil, fmt.Errorf("creating data dir: %w", err)
	}

	dbPath := filepath.Join(dataDir, "power-panel.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	db.SetMaxOpenConns(1)
	db.SetConnMaxIdleTime(time.Minute)

	if _, err := db.ExecContext(ctx, `
		PRAGMA journal_mode = WAL;
		PRAGMA synchronous = NORMAL;
		PRAGMA busy_timeout = 5000;
		PRAGMA foreign_keys = ON;
		PRAGMA temp_store = MEMORY;
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("setting pragmas: %w", err)
	}

	if _, err := db.ExecContext(ctx, schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating schema: %w", err)
	}

	// Retention: delete events older than 1 year
	if _, err := db.ExecContext(ctx, "DELETE FROM power_events WHERE timestamp < datetime('now', '-1 year')"); err != nil {
		db.Close()
		return nil, fmt.Errorf("cleaning old events: %w", err)
	}

	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) LogEvent(ctx context.Context, ev PowerEvent) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO power_events (action, user_login, user_name, success, error_msg, duration_ms)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		ev.Action, ev.UserLogin, ev.UserName, ev.Success, ev.ErrorMsg, ev.DurationMS,
	)
	if err != nil {
		return fmt.Errorf("logging power event: %w", err)
	}
	return nil
}

func (s *Store) RecentEvents(ctx context.Context, limit int) ([]PowerEvent, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, timestamp, action, user_login, user_name, success, error_msg, duration_ms
		 FROM power_events ORDER BY timestamp DESC LIMIT ?`, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []PowerEvent
	for rows.Next() {
		var ev PowerEvent
		var ts string
		var errMsg sql.NullString
		var durMS sql.NullInt64

		if err := rows.Scan(&ev.ID, &ts, &ev.Action, &ev.UserLogin, &ev.UserName, &ev.Success, &errMsg, &durMS); err != nil {
			return nil, err
		}

		ev.Timestamp, err = time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			if ev.Timestamp, err = time.Parse("2006-01-02T15:04:05.000Z", ts); err != nil {
				slog.Warn("unparseable event timestamp", "raw", ts, "event_id", ev.ID)
			}
		}
		if errMsg.Valid {
			ev.ErrorMsg = errMsg.String
		}
		if durMS.Valid {
			ev.DurationMS = durMS.Int64
		}
		events = append(events, ev)
	}
	return events, rows.Err()
}
