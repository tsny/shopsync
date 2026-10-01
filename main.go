// main.go
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	_ "time/tzdata"

	"cloud.google.com/go/storage"
	"github.com/PuerkitoBio/goquery"
	"github.com/joho/godotenv"
	"github.com/tsny/shopsync/pkg/icalplayers"
	"github.com/tsny/shopsync/pkg/imgproc"
	"github.com/tsny/shopsync/pkg/showstore"
	"github.com/tsny/shopsync/pkg/wpevents"
	"github.com/tsny/shopsync/pkg/wpimg"
)

func main() {
	src := flag.String("src", "", "Path or URL to an .ics file. Use '-' to read from stdin")
	wpURL := flag.String("wp", "", "URL to WordPress tribe/events API (e.g. https://theimprovshop.com/wp-json/tribe/events/v1/events)")
	wpCache := flag.String("wp-cache", "", "Path to cached WP events JSON; skips live fetch when set")
	postURL := flag.String("post-url", "", "testing param: grabs image from given post URL")
	skipImageSearch := flag.Bool("skip-image-search", false, "If set, do not attempt to fetch post images")
	useTeamsFile := flag.Bool("use-teams-file", false, "If set, parse teams from teams.txt and match to events")
	dryRun := flag.Bool("dry-run", true, "If set, do not store events in the database")
	printSummary := flag.Bool("summary", false, "If set, print a summary of events after parsing")
	gcsBucket := flag.String("gcs-bucket", "improv-wiki-teams", "GCS bucket for converted WebP images")
	gcsPrefix := flag.String("gcs-prefix", "shows/res/", "GCS object prefix for uploaded images")
	flag.Parse()

	if *skipImageSearch {
		icalplayers.SkipImageSearch = true
	}

	_ = godotenv.Load()

	if postURL != nil && *postURL != "" {
		// https://theimprovshop.com/show/teams-level-2-student-showcase-16/
		res, err := wpimg.Fetch(context.Background(), *postURL)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println("Fetched image:", res.ImageURL)
		return
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL missing")
	}

	ctx := context.Background()
	store, err := showstore.Open(ctx, dbURL)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()

	if err := store.MigrateSyncRuns(ctx); err != nil {
		exitErr(fmt.Errorf("migrate sync_runs: %w", err))
	}
	syncStore = store
	syncRun = &showstore.SyncRun{StartedAt: time.Now(), DryRun: *dryRun, Source: "ics"}
	switch {
	case *wpCache != "":
		syncRun.Source = "wp-cache"
	case *wpURL != "":
		syncRun.Source = "wp"
	}
	defer finishSyncRun(nil)

	var events []icalplayers.Event

	const defaultWPCacheFile = "wp_events_cache.json"

	if *wpCache != "" {
		fmt.Printf("Loading WP events from cache: %s\n", *wpCache)
		events, err = wpevents.LoadCache(*wpCache)
		if err != nil {
			exitErr(fmt.Errorf("wp cache load: %w", err))
		}
		fmt.Printf("Loaded %d events from cache.\n", len(events))
	} else if *wpURL != "" {
		// Fetch events from the WordPress tribe/events API
		events, err = wpevents.FetchAll(ctx, *wpURL)
		if err != nil {
			exitErr(fmt.Errorf("wp fetch: %w", err))
		}
		if err = wpevents.SaveCache(defaultWPCacheFile, events); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not save WP cache: %v\n", err)
		} else {
			fmt.Printf("Saved WP events cache to %s\n", defaultWPCacheFile)
		}
	} else {
		var calendarURL string
		if *src == "" {
			// Query the page to find the Google Calendar URL
			fmt.Println("No -src provided, fetching calendar URL from page...")
			pageURL := "https://theimprovshop.com/show-calendar/list/?tribe_paged=1&tribe_event_display=list&tribe_venues=233"
			calendarURL, err = extractGoogleCalendarURL(ctx, pageURL)
			if err != nil {
				exitErr(fmt.Errorf("failed to extract calendar URL: %w", err))
			}
			fmt.Printf("Found calendar URL: %s\n", calendarURL)
		} else {
			calendarURL = *src
		}

		if isURL(calendarURL) {
			fmt.Printf("Reading ICS from URL: %s\n", calendarURL)
			events, err = icalplayers.FromURL(context.Background(), calendarURL, http.DefaultClient, nil)
			if err != nil {
				exitErr(err)
			}
		} else {
			fmt.Printf("Reading ICS from file: %s\n", calendarURL)
			events, err = icalplayers.FromFile(calendarURL, nil)
			if err != nil {
				exitErr(err)
			}
		}
	}

	syncRun.EventsFetched = len(events)

	if len(events) == 0 {
		fmt.Println("No events found")
		return
	}

	var teams []showstore.Team
	if *useTeamsFile {
		teamList, err := ReadLinesToArray("teams.txt")
		if err != nil {
			exitErr(err)
		}
		for _, t := range teamList {
			teams = append(teams, showstore.Team{Name: t})
		}
	} else {
		teams, err = store.GetAllTeams(ctx)
		if err != nil {
			exitErr(err)
		}
		fmt.Printf("Loaded %d teams from database.\n", len(teams))
	}

	for i, ev := range events {
		parsedTeams := findTeamsInEventDescription(ev.Description, teams)
		if len(parsedTeams) > 0 {
			syncRun.EventsWithTeams++
			fmt.Printf("%v: %v\n", ev.Summary, parsedTeams)
			for _, t := range parsedTeams {
				if t.ID == "" {
					fmt.Printf("Skipping team with empty ID: %s\n", t.Name)
					return
				}
				events[i].TeamIDs = append(events[i].TeamIDs, t.ID)
				events[i].Teams = append(events[i].Teams, t.Name)
			}
		} else {
			fmt.Printf("Event %s matches no teams.\n", ev.Summary)
		}
	}

	for i, ev := range events {
		if ev.PostImageURL != "" {
			events[i].PostImageURL = wpevents.RewriteCdnCgiURL(ev.PostImageURL)
		}
	}

	if *printSummary {
		icalplayers.SummarizeEvents(events)
	}

	if *dryRun {
		fmt.Println("Dry run; not storing events.")
		return
	}

	var gcs *storage.Client
	gcs, err = storage.NewClient(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: GCS client init failed (%v); images will not be uploaded\n", err)
		gcs = nil
	} else {
		defer gcs.Close()
	}

	if *wpURL != "" || *wpCache != "" {
		// Match incoming events to existing rows before changing anything.
		// A row with the event's WP UID wins outright, however far the show
		// moved or whatever it was renamed to. Rows imported via ICS or
		// showtool have other UIDs, so fall back to (normalized summary,
		// within ±12h); exact-time matches are claimed first so that when the
		// source moves a show's time, a same-named show on the same day can't
		// steal its row. New events go through InsertIfNew, which applies the
		// same (date, summary) dedup.
		existingFor := make([]*icalplayers.Event, len(events))
		var claimed []string
		for i, e := range events {
			m, err := store.FindByUID(ctx, e.UID)
			if err != nil {
				exitErr(err)
			}
			if m != nil {
				existingFor[i] = m
				claimed = append(claimed, m.UID)
			}
		}
		for _, exactOnly := range []bool{true, false} {
			for i, e := range events {
				if existingFor[i] != nil {
					continue
				}
				m, err := store.FindByDateAndSummary(ctx, e.Start, e.Summary, claimed...)
				if err != nil {
					exitErr(err)
				}
				if m == nil || (exactOnly && !m.Start.Equal(*e.Start)) {
					continue
				}
				existingFor[i] = m
				claimed = append(claimed, m.UID)
			}
		}

		var inserted, updated, skipped int
		for i, e := range events {
			existing := existingFor[i]
			if existing == nil {
				// New event: convert and upload image to GCS before inserting.
				if e.PostImageURL != "" && gcs != nil {
					e.PostImageURL = toGCSImage(ctx, gcs, e.PostImageURL, e.UID, *gcsBucket, *gcsPrefix)
				}
				ok, err := store.InsertIfNew(ctx, e)
				if err != nil {
					exitErr(err)
				}
				if ok {
					inserted++
					fmt.Printf("Inserted: %s (%s)\n", e.Summary, e.Start)
				} else {
					fmt.Printf("%v already exists, skipping insert: %s (%s)\n", e.Start, e.Summary, e.UID)
				}
				continue
			}
			// The source is authoritative for start times; they get edited after posting.
			timeChanged := e.Start != nil && !existing.Start.Equal(*e.Start)
			// Only a UID match can differ beyond punctuation/case; leave those alone.
			renamed := normalizeTitle(existing.Summary) != normalizeTitle(e.Summary)
			descChanged := existing.Description != e.Description
			teamsChanged := !teamsEqualSorted(existing.Teams, e.Teams)
			// If the incoming image differs, upload it; otherwise ensure the
			// stored image is already in GCS (migrate it if not).
			imageNeedsUpdate := false
			if gcs != nil {
				if e.PostImageURL != "" && existing.PostImageURL != e.PostImageURL {
					e.PostImageURL = toGCSImage(ctx, gcs, e.PostImageURL, existing.UID, *gcsBucket, *gcsPrefix)
					imageNeedsUpdate = e.PostImageURL != existing.PostImageURL
				} else if existing.PostImageURL != "" && !imgproc.IsGCSURL(existing.PostImageURL, *gcsBucket) {
					newURL := toGCSImage(ctx, gcs, existing.PostImageURL, existing.UID, *gcsBucket, *gcsPrefix)
					if newURL != existing.PostImageURL {
						if err := store.UpdateShowImageURL(ctx, existing.UID, newURL); err != nil {
							fmt.Fprintf(os.Stderr, "  image db update error: %v\n", err)
						} else {
							fmt.Printf("  Migrated image to GCS: %s\n", existing.Summary)
						}
					}
				}
			} else {
				imageNeedsUpdate = e.PostImageURL != "" && existing.PostImageURL != e.PostImageURL
			}
			if !timeChanged && !renamed && !descChanged && !teamsChanged && !imageNeedsUpdate {
				skipped++
				fmt.Printf("Unchanged: %s (%s)\n", e.Summary, e.Start)
				continue
			}
			fmt.Printf("Updating: %s (%s)\n", e.Summary, e.Start)
			if timeChanged {
				fmt.Printf("  start: %s -> %s\n", existing.Start, e.Start)
			}
			if renamed {
				fmt.Printf("  summary: %q -> %q\n", existing.Summary, e.Summary)
			}
			if descChanged {
				fmt.Printf("  description: %q\n            -> %q\n",
					truncateStr(existing.Description, 80), truncateStr(e.Description, 80))
			}
			if teamsChanged {
				fmt.Printf("  teams: %v -> %v\n", existing.Teams, e.Teams)
			}
			if imageNeedsUpdate {
				fmt.Printf("  image: %s -> %s\n", existing.PostImageURL, e.PostImageURL)
			}
			if timeChanged || renamed {
				newStart, newSummary := *existing.Start, existing.Summary
				if timeChanged {
					newStart = *e.Start
				}
				if renamed {
					newSummary = e.Summary
				}
				if err := store.UpdateShowStartAndSummary(ctx, existing.UID, newStart, newSummary); err != nil {
					exitErr(err)
				}
			}
			if descChanged || teamsChanged {
				if err := store.UpdateDescriptionAndTeams(ctx, existing.UID, e.Description, e.Teams, e.TeamIDs); err != nil {
					exitErr(err)
				}
			}
			if imageNeedsUpdate {
				if err := store.UpdateShowImageURL(ctx, existing.UID, e.PostImageURL); err != nil {
					exitErr(err)
				}
			}
			updated++
		}
		fmt.Printf("Inserted %d, updated %d, unchanged %d.\n", inserted, updated, skipped)
		syncRun.Inserted, syncRun.Updated, syncRun.Unchanged = inserted, updated, skipped

		// Delete upcoming DB shows that no longer appear in the fetched WP events.
		upcoming, err := store.GetUpcomingShows(ctx)
		if err != nil {
			exitErr(fmt.Errorf("get upcoming shows: %w", err))
		}
		var deleted int
		for _, dbShow := range upcoming {
			if dbShow.Start == nil {
				continue
			}
			if matchesAny(dbShow, events) {
				continue
			}
			// Manually-added shows never appear in WP events; keep them.
			if strings.Contains(strings.ToLower(dbShow.Description), "manual add") {
				continue
			}
			fmt.Printf("Deleting stale show: %s (%s)\n", dbShow.Summary, dbShow.Start)
			if !*dryRun {
				if err := store.DeleteShow(ctx, dbShow.UID); err != nil {
					exitErr(fmt.Errorf("delete show %s: %w", dbShow.UID, err))
				}
			}
			deleted++
		}
		if deleted > 0 {
			fmt.Printf("Deleted %d stale show(s).\n", deleted)
		}
		syncRun.Deleted = deleted
	} else {
		for _, e := range events {
			if err := store.Upsert(ctx, e); err != nil {
				exitErr(err)
			}
		}
		fmt.Printf("Stored %d events.\n", len(events))
		syncRun.Updated = len(events)
	}
}

var nonAlphanumRe = regexp.MustCompile(`[^a-zA-Z0-9 ]`)

func normalizeTitle(s string) string {
	return strings.ToLower(nonAlphanumRe.ReplaceAllString(s, ""))
}

// matchesAny reports whether dbShow has a counterpart in candidates, either by
// UID or by the same (date ±12h, normalized summary) rule used in FindByDateAndSummary.
func matchesAny(dbShow icalplayers.Event, candidates []icalplayers.Event) bool {
	norm := normalizeTitle(dbShow.Summary)
	for _, c := range candidates {
		if c.UID == dbShow.UID {
			return true
		}
		if c.Start == nil {
			continue
		}
		diff := dbShow.Start.Sub(*c.Start)
		if diff < 0 {
			diff = -diff
		}
		if diff <= 12*time.Hour && normalizeTitle(c.Summary) == norm {
			return true
		}
	}
	return false
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func teamsEqualSorted(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ac, bc := make([]string, len(a)), make([]string, len(b))
	copy(ac, a)
	copy(bc, b)
	sort.Strings(ac)
	sort.Strings(bc)
	for i := range ac {
		if ac[i] != bc[i] {
			return false
		}
	}
	return true
}

func isURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme != "" && u.Host != ""
}

// Metrics for the current run. Nil until the store is open.
var (
	syncStore *showstore.Store
	syncRun   *showstore.SyncRun
)

// finishSyncRun records the current run once. A nil err marks it successful.
func finishSyncRun(err error) {
	if syncRun == nil {
		return
	}
	run := *syncRun
	syncRun = nil

	run.FinishedAt = time.Now()
	run.Status = "success"
	if err != nil {
		run.Status = "error"
		run.Error = err.Error()
	}
	if rerr := syncStore.RecordSyncRun(context.Background(), run); rerr != nil {
		fmt.Fprintf(os.Stderr, "warning: could not record sync run: %v\n", rerr)
	}
}

func exitErr(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	finishSyncRun(err)
	os.Exit(1)
}

// findTeamsInEventDescription from event description
func findTeamsInEventDescription(desc string, teams []showstore.Team) []showstore.Team {
	var matches []showstore.Team
	for _, t := range teams {
		if len(t.Name) <= 4 { // skip short/generic names
			continue
		}
		if strings.Contains(desc, t.Name) {
			matches = append(matches, t)
		}
	}
	return matches
}

// read new line separated file into array
func ReadLinesToArray(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}

// toGCSImage converts an external image URL to WebP, uploads it to GCS, and
// returns the new GCS URL. Falls back to the original URL on any error.
func toGCSImage(ctx context.Context, gcs *storage.Client, imageURL, uid, bucket, prefix string) string {
	if imgproc.IsGCSURL(imageURL, bucket) {
		return imageURL
	}
	object := prefix + uid + ".webp"
	gcsURL, err := imgproc.ConvertAndUpload(ctx, gcs, imageURL, bucket, object)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  image upload failed for %s: %v\n", uid, err)
		if syncRun != nil {
			syncRun.ImageFailures++
		}
		return imageURL
	}
	if syncRun != nil {
		syncRun.ImagesUploaded++
	}
	fmt.Printf("  Uploaded image to GCS: %s\n", gcsURL)
	return gcsURL
}

// extractGoogleCalendarURL fetches the page and extracts the calendar URL from the Google Calendar link
func extractGoogleCalendarURL(ctx context.Context, pageURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", pageURL, nil)
	if err != nil {
		return "", err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return "", err
	}

	// Find the Google Calendar link
	var calendarURL string
	doc.Find("a").Each(func(i int, s *goquery.Selection) {
		text := strings.TrimSpace(s.Text())
		if strings.Contains(text, "Google Calendar") {
			href, exists := s.Attr("href")
			if exists {
				calendarURL = href
			}
		}
	})

	if calendarURL == "" {
		return "", errors.New("Google Calendar link not found on page")
	}

	// Parse the Google Calendar URL to extract the cid parameter
	parsedURL, err := url.Parse(calendarURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse Google Calendar URL: %w", err)
	}

	cid := parsedURL.Query().Get("cid")
	if cid == "" {
		return "", errors.New("cid parameter not found in Google Calendar URL")
	}

	// URL decode the cid parameter
	decodedCID, err := url.QueryUnescape(cid)
	if err != nil {
		return "", fmt.Errorf("failed to decode cid parameter: %w", err)
	}

	// Parse the decoded webcal URL
	webcalURL, err := url.Parse(decodedCID)
	if err != nil {
		return "", fmt.Errorf("failed to parse webcal URL: %w", err)
	}

	// Convert webcal:// to https:// to get the actual .ics file URL
	if webcalURL.Scheme == "webcal" {
		webcalURL.Scheme = "https"
	}

	return webcalURL.String(), nil
}
