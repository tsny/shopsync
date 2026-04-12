package showstore

import (
	"context"
	"errors"
	"time"

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

// ShowWithImageURL represents a show with its image URL status
type ShowWithImageURL struct {
	UID          string
	Summary      string
	PostImageURL *string // nil if not set
}

// GetShowsWithoutImageURL returns all shows that don't have a post_image_url set
func (s *Store) GetShowsWithoutImageURL(ctx context.Context) ([]ShowWithImageURL, error) {
	const q = `
SELECT uid, summary, post_image_url
FROM shows
WHERE post_image_url IS NULL OR post_image_url = ''
ORDER BY start NULLS LAST;
`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ShowWithImageURL
	for rows.Next() {
		var show ShowWithImageURL
		var postImageURL *string
		if err := rows.Scan(&show.UID, &show.Summary, &postImageURL); err != nil {
			return nil, err
		}
		show.PostImageURL = postImageURL
		out = append(out, show)
	}
	if rows.Err() != nil {
		return nil, rows.Err()
	}
	return out, nil
}

// GetShowsWithCdnCgiURL returns shows whose post_image_url contains cdn-cgi/imagedelivery.
func (s *Store) GetShowsWithCdnCgiURL(ctx context.Context) ([]ShowWithImageURL, error) {
	const q = `
SELECT uid, summary, post_image_url
FROM shows
WHERE post_image_url LIKE '%cdn-cgi/imagedelivery%'
ORDER BY start NULLS LAST;
`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ShowWithImageURL
	for rows.Next() {
		var show ShowWithImageURL
		var postImageURL *string
		if err := rows.Scan(&show.UID, &show.Summary, &postImageURL); err != nil {
			return nil, err
		}
		show.PostImageURL = postImageURL
		out = append(out, show)
	}
	if rows.Err() != nil {
		return nil, rows.Err()
	}
	return out, nil
}

// UpdateShowImageURL updates the post_image_url for a show by its UID
func (s *Store) UpdateShowImageURL(ctx context.Context, uid, imageURL string) error {
	const q = `
UPDATE shows
SET post_image_url = $1, updated_at = NOW()
WHERE uid = $2;
`
	_, err := s.pool.Exec(ctx, q, imageURL, uid)
	return err
}

// UpdateAllTimesToPM updates all show start times to PM
// Times that are AM (0-11 hours) will have 12 hours added to become PM
// Times that are already PM (12-23 hours) will remain unchanged
// Uses America/Chicago timezone for hour extraction
func (s *Store) UpdateAllTimesToPM(ctx context.Context) (int64, error) {
	const q = `
UPDATE shows
SET start = start + INTERVAL '12 hours',
    updated_at = NOW()
WHERE start IS NOT NULL
  AND EXTRACT(HOUR FROM start AT TIME ZONE 'America/Chicago') < 12;
`
	result, err := s.pool.Exec(ctx, q)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}

// UpdateAllTimesToPMForce updates ALL show start times to PM regardless of current state
// This ensures every time is in the PM range (12:00 PM - 11:59 PM)
func (s *Store) UpdateAllTimesToPMForce(ctx context.Context) (int64, error) {
	const q = `
UPDATE shows
SET start = CASE 
    WHEN EXTRACT(HOUR FROM start AT TIME ZONE 'UTC') < 12 THEN 
        start + INTERVAL '12 hours'
    ELSE 
        start
    END,
    updated_at = NOW()
WHERE start IS NOT NULL
  AND EXTRACT(HOUR FROM start AT TIME ZONE 'UTC') < 12;
`
	result, err := s.pool.Exec(ctx, q)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}

// MoveFutureShowsBackOneDay moves all future shows back by 1 day
func (s *Store) MoveFutureShowsBackOneDay(ctx context.Context) (int64, error) {
	const q = `
UPDATE shows
SET start = start - INTERVAL '1 day',
    updated_at = NOW()
WHERE start IS NOT NULL
  AND start > NOW();
`
	result, err := s.pool.Exec(ctx, q)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
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

// ExistsByDateAndSummary checks if a show exists with the same start date and summary
func (s *Store) ExistsByDateAndSummary(ctx context.Context, start *time.Time, summary string) (bool, error) {
	if start == nil {
		return false, nil
	}
	// Normalize both sides: lowercase, strip non-alphanumeric, collapse whitespace.
	// This catches HTML-entity differences, punctuation variance, and case mismatches.
	const q = `
SELECT EXISTS(
  SELECT 1 FROM shows
  WHERE DATE(start) = DATE($1::TIMESTAMPTZ)
    AND lower(regexp_replace(summary,     '[^a-zA-Z0-9 ]', '', 'g')) =
        lower(regexp_replace($2::text,    '[^a-zA-Z0-9 ]', '', 'g'))
)
`
	var exists bool
	err := s.pool.QueryRow(ctx, q, start, summary).Scan(&exists)
	return exists, err
}

// FindByDateAndSummary returns the existing show's uid, description, and teams if found.
func (s *Store) FindByDateAndSummary(ctx context.Context, start *time.Time, summary string) (*icalplayers.Event, error) {
	if start == nil {
		return nil, nil
	}
	const q = `
SELECT uid, description, teams
FROM shows
WHERE DATE(start) = DATE($1::TIMESTAMPTZ)
  AND lower(regexp_replace(summary,  '[^a-zA-Z0-9 ]', '', 'g')) =
      lower(regexp_replace($2::text, '[^a-zA-Z0-9 ]', '', 'g'))
LIMIT 1
`
	var e icalplayers.Event
	var teams []string
	err := s.pool.QueryRow(ctx, q, start, summary).Scan(&e.UID, &e.Description, &teams)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	e.Teams = teams
	return &e, nil
}

// UpdateDescriptionAndTeams updates an existing show's description and teams by UID.
func (s *Store) UpdateDescriptionAndTeams(ctx context.Context, uid, description string, teams []string, teamIDs []string) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()

	// Union incoming teams with existing ones so DB teams are never removed.
	const q = `
UPDATE shows
SET description = $1,
    teams       = (SELECT ARRAY(SELECT DISTINCT unnest(teams || $2::text[]))),
    updated_at  = NOW()
WHERE uid = $3
`
	_, err = tx.Exec(ctx, q, description, strSliceToTextArray(teams), uid)
	if err != nil {
		return err
	}

	// syncShowTeams uses ON CONFLICT DO NOTHING, so existing rows are preserved.
	if err = syncShowTeams(ctx, tx, uid, teamIDs); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// InsertIfNew inserts a show only if no show exists with the same date and summary.
// Returns (inserted bool, error).
func (s *Store) InsertIfNew(ctx context.Context, e icalplayers.Event) (bool, error) {
	// Check if already exists
	exists, err := s.ExistsByDateAndSummary(ctx, e.Start, e.Summary)
	if err != nil {
		return false, err
	}
	if exists {
		return false, nil // Already exists, skip
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()

	const insertShow = `
INSERT INTO shows (uid, summary, description, url, post_image_url, start, players, teams, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW(), NOW())
ON CONFLICT (uid) DO NOTHING
`
	result, err := tx.Exec(ctx, insertShow,
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
		return false, err
	}

	// Check if row was actually inserted
	if result.RowsAffected() == 0 {
		_ = tx.Rollback(ctx)
		return false, nil
	}

	if err = syncShowTeams(ctx, tx, e.UID, e.TeamIDs); err != nil {
		return false, err
	}

	return true, tx.Commit(ctx)
}
