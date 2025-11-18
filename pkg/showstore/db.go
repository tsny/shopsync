package showstore

import (
	"context"

	"github.com/jackc/pgx/v5"
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
  uid            TEXT PRIMARY KEY,
  summary        TEXT NOT NULL,
  description    TEXT NOT NULL,
  url            TEXT,
  post_image_url TEXT,
  start          TIMESTAMPTZ,
  players        TEXT[] DEFAULT '{}',
  teams          TEXT[] DEFAULT '{}',
  addl_teams     TEXT[] DEFAULT '{}',
  created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS show_teams (
  show_uid TEXT NOT NULL REFERENCES shows(uid) ON DELETE CASCADE,
  team_id  TEXT NOT NULL REFERENCES "Team"(id) ON DELETE CASCADE,
  PRIMARY KEY (show_uid, team_id)
);

CREATE INDEX IF NOT EXISTS show_teams_team_id_idx ON show_teams(team_id);
CREATE INDEX IF NOT EXISTS shows_start_idx ON shows (start);
`
	_, err := s.pool.Exec(ctx, q)
	return err
}

func (s *Store) DeletePastEvents(ctx context.Context) error {
	const q = `
DELETE FROM shows
WHERE start < NOW();
`
	_, err := s.pool.Exec(ctx, q)
	return err
}

// UpsertShow inserts or updates a single event.
// Now includes the URL field.
func (s *Store) Upsert(ctx context.Context, e icalplayers.Event) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()

	const upsertShow = `
INSERT INTO shows (uid, summary, description, url, post_image_url, start, players, teams, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW(), NOW())
ON CONFLICT (uid) DO UPDATE
SET summary        = EXCLUDED.summary,
    description    = EXCLUDED.description,
    url            = EXCLUDED.url,
    post_image_url = EXCLUDED.post_image_url,
    start          = EXCLUDED.start,
    players        = EXCLUDED.players,
    teams          = EXCLUDED.teams,
    updated_at     = NOW();
`

	_, err = tx.Exec(ctx, upsertShow,
		e.UID,
		e.Summary,
		e.Description,
		e.URL,
		e.PostImageURL,
		e.Start,
		strSliceToTextArray(e.Players),
		strSliceToTextArray(e.Teams),
	)
	if err != nil {
		return err
	}

	if err = syncShowTeams(ctx, tx, e.UID, e.TeamIDs); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func syncShowTeams(ctx context.Context, tx pgx.Tx, showUID string, teamIDs []string) error {
	if len(teamIDs) == 0 {
		return nil
	}

	const q = `
INSERT INTO show_teams (show_uid, team_id)
VALUES ($1, $2)
ON CONFLICT (show_uid, team_id) DO NOTHING
`

	for _, id := range teamIDs {
		if _, err := tx.Exec(ctx, q, showUID, id); err != nil {
			return err
		}
	}

	return nil
}

// Helper: TEXT[] wants []string; pgx will map it automatically.
// This wrapper exists in case you want to pre-normalize.
func strSliceToTextArray(in []string) []string {
	// Optionally trim/unique here.
	return in
}

func (s *Store) GetAllTeams(ctx context.Context) ([]Team, error) {
	const q = `
SELECT name, id
FROM "Team"
`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Team
	for rows.Next() {
		var t Team
		if err := rows.Scan(&t.Name, &t.ID); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	if rows.Err() != nil {
		return nil, rows.Err()
	}
	return out, nil
}

func (s *Store) GetAllShows(ctx context.Context) ([]icalplayers.Event, error) {
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
	const q = `
DROP TABLE IF EXISTS show_teams;
DROP TABLE IF EXISTS shows;
`
	_, err := s.pool.Exec(ctx, q)
	return err
}
