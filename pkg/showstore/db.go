package showstore

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tsny/shopsync/pkg/icalplayers"
)

type Store struct {
	pool *pgxpool.Pool
}

// Open connects to Postgres using a standard URL, e.g.:
// postgres://user:pass@host:5432/dbname?sslmode=disable
func Open(ctx context.Context, url string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

// Migrate creates the table and indexes if they do not exist.
// Note: use "description" not "desc" (DESC is a keyword).
func (s *Store) Migrate(ctx context.Context) error {
	const q = `
CREATE TABLE IF NOT EXISTS shows (
  uid TEXT PRIMARY KEY,
  summary TEXT NOT NULL,
  description TEXT NOT NULL,
	url TEXT,
  start TIMESTAMPTZ,
  players TEXT[] NOT NULL DEFAULT '{}',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS shows_start_idx ON shows (start);
`
	_, err := s.pool.Exec(ctx, q)
	return err
}

// Upsert inserts or updates a single event.
// Now includes the URL field.
func (s *Store) Upsert(ctx context.Context, e icalplayers.Event) error {
	const q = `
INSERT INTO shows (uid, summary, description, url, start, players, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, NOW(), NOW())
ON CONFLICT (uid) DO UPDATE
SET summary     = EXCLUDED.summary,
    description = EXCLUDED.description,
    url         = EXCLUDED.url,
    start       = EXCLUDED.start,
    players     = EXCLUDED.players,
    updated_at  = NOW();
`
	_, err := s.pool.Exec(ctx, q,
		e.UID,
		e.Summary,
		e.Description,
		e.URL,
		e.Start,
		strSliceToTextArray(e.Players),
	)
	return err
}

// Helper: TEXT[] wants []string; pgx will map it automatically.
// This wrapper exists in case you want to pre-normalize.
func strSliceToTextArray(in []string) []string {
	// Optionally trim/unique here.
	return in
}

func (s *Store) GetAll(ctx context.Context) ([]icalplayers.Event, error) {
	const q = `
SELECT uid, summary, description, start, players
FROM shows
ORDER BY start NULLS LAST;
`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []icalplayers.Event
	for rows.Next() {
		var e icalplayers.Event
		var players []string
		if err := rows.Scan(&e.UID, &e.Summary, &e.Description, &e.Start, &players); err != nil {
			return nil, err
		}
		e.Players = players
		out = append(out, e)
	}
	if rows.Err() != nil {
		return nil, rows.Err()
	}
	return out, nil
}

// In showstore/store.go
func (s *Store) Drop(ctx context.Context) error {
	const q = `DROP TABLE IF EXISTS shows;`
	_, err := s.pool.Exec(ctx, q)
	return err
}
