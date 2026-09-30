package showstore

import (
	"context"
	"time"
)

// SyncRun holds metrics for one run of the primary sync job.
type SyncRun struct {
	StartedAt  time.Time
	FinishedAt time.Time
	Source     string // wp, wp-cache, ics
	DryRun     bool
	Status     string // success, error
	Error      string

	EventsFetched   int
	EventsWithTeams int
	Inserted        int
	Updated         int
	Unchanged       int
	Deleted         int
	ImagesUploaded  int
	ImageFailures   int
}

// MigrateSyncRuns creates the sync_runs table if it does not exist.
func (s *Store) MigrateSyncRuns(ctx context.Context) error {
	const q = `
CREATE TABLE IF NOT EXISTS sync_runs (
  id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  started_at        TIMESTAMPTZ NOT NULL,
  finished_at       TIMESTAMPTZ NOT NULL,
  duration_ms       BIGINT NOT NULL,
  source            TEXT NOT NULL,
  dry_run           BOOL NOT NULL,
  status            TEXT NOT NULL,
  error             TEXT,
  events_fetched    INT NOT NULL DEFAULT 0,
  events_with_teams INT NOT NULL DEFAULT 0,
  inserted          INT NOT NULL DEFAULT 0,
  updated           INT NOT NULL DEFAULT 0,
  unchanged         INT NOT NULL DEFAULT 0,
  deleted           INT NOT NULL DEFAULT 0,
  images_uploaded   INT NOT NULL DEFAULT 0,
  image_failures    INT NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS sync_runs_started_at_idx ON sync_runs (started_at);
`
	_, err := s.pool.Exec(ctx, q)
	return err
}

// RecordSyncRun inserts one row into sync_runs.
func (s *Store) RecordSyncRun(ctx context.Context, r SyncRun) error {
	const q = `
INSERT INTO sync_runs (
  started_at, finished_at, duration_ms, source, dry_run, status, error,
  events_fetched, events_with_teams, inserted, updated, unchanged, deleted,
  images_uploaded, image_failures
) VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''), $8, $9, $10, $11, $12, $13, $14, $15)
`
	_, err := s.pool.Exec(ctx, q,
		r.StartedAt, r.FinishedAt, r.FinishedAt.Sub(r.StartedAt).Milliseconds(),
		r.Source, r.DryRun, r.Status, r.Error,
		r.EventsFetched, r.EventsWithTeams, r.Inserted, r.Updated, r.Unchanged, r.Deleted,
		r.ImagesUploaded, r.ImageFailures,
	)
	return err
}
