# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

Shopsync syncs upcoming show data for The Improv Shop (an improv comedy venue) into a CockroachDB/Postgres database. It is a collection of small CLI tools that share a common `pkg/` library.

## Commands

```bash
# Build and run a specific tool
go run ./showtool/         # Parse TSV and insert shows into DB
go run ./picturematcher/   # Match show names to GCS image URLs and update DB
go run ./updatetimes/      # Fix AM times to PM in the DB
go run ./movedates/        # Move all future shows back by 1 day

# Build all
go build ./...

# Run tests
go test ./...

# Run a single package's tests
go test ./pkg/icalplayers/
```

All tools that write to the DB default to `-dry-run=true`. Pass `-dry-run=false` to actually apply changes.

## Environment

`DATABASE_URL` must be set (CockroachDB connection string). The root `.envrc` is loaded by direnv automatically. Some tools (`showtool`) also try to load `../.env` relative to their directory.

## Architecture

### Packages (`pkg/`)

- **`pkg/icalplayers`** — Core `Event` type used everywhere. Parses `.ics` calendar files, fetches from URLs, and infers player names from event descriptions using regex heuristics. Also calls `wpimg` to fetch post images during iCal parsing.
- **`pkg/showstore`** — All Postgres/CockroachDB access via `pgx/v5`. `Store` wraps a connection pool. Key operations: `Upsert`, `InsertIfNew` (deduplicates by date+summary), `Migrate` (creates schema), `GetAllTeams`, `GetAllShows`, `UpdateShowImageURL`.
- **`pkg/wpevents`** — Fetches events from the WordPress `tribe/events/v1/events` REST API, paginating via `next_rest_url`. Converts to `icalplayers.Event`.
- **`pkg/wpimg`** — Scrapes the `<img class="wp-post-image">` from a WordPress post page to get the featured image URL.

### CLI tools

- **`showtool/`** — Reads `_private/IS SHOWS 2025 - Untitled.tsv` (date/time/venue/show/teams columns), parses it, matches teams against the `Team` table in the DB, and inserts new shows via `store.InsertIfNew`. Writes a `shows_parsed.tsv` output for inspection. UIDs are SHA256 hashes of date+summary+venue+line number.
- **`picturematcher/`** — Queries DB for shows missing `post_image_url`, normalizes show names to slugs, looks up a hardcoded map of slug→GCS filename, and updates the DB. Image base URL: `https://storage.googleapis.com/improv-wiki-teams/shows/res/`.
- **`updatetimes/`** — Calls `store.UpdateAllTimesToPM` to shift any AM timestamps to PM (America/Chicago).
- **`movedates/`** — Calls `store.MoveFutureShowsBackOneDay` to subtract 1 day from all future show start times.

### Database schema

```sql
shows (uid PK, summary, description, url, post_image_url, start TIMESTAMPTZ, players TEXT[], teams TEXT[], created_at, updated_at)
show_teams (show_uid FK, team_id FK)  -- junction table
"Team" (id, name)  -- pre-existing table, note quoted name
```

Deduplication in `InsertIfNew` normalizes both sides: strips non-alphanumeric, lowercases, compares date and summary. `Upsert` does a full ON CONFLICT update by UID.
