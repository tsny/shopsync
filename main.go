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
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/joho/godotenv"
	"github.com/tsny/shopsync/pkg/icalplayers"
	"github.com/tsny/shopsync/pkg/showstore"
	"github.com/tsny/shopsync/pkg/wpevents"
	"github.com/tsny/shopsync/pkg/wpimg"
)

func main() {
	src := flag.String("src", "", "Path or URL to an .ics file. Use '-' to read from stdin")
	wpURL := flag.String("wp", "", "URL to WordPress tribe/events API (e.g. https://theimprovshop.com/wp-json/tribe/events/v1/events)")
	postURL := flag.String("post-url", "", "testing param: grabs image from given post URL")
	deletePastEvents := flag.Bool("delete-past-events", false, "If set, delete past events from the database")
	skipImageSearch := flag.Bool("skip-image-search", false, "If set, do not attempt to fetch post images")
	useTeamsFile := flag.Bool("use-teams-file", false, "If set, parse teams from teams.txt and match to events")
	recreateDB := flag.Bool("recreate-db", false, "If set, drop and recreate the database tables")
	dryRun := flag.Bool("dry-run", true, "If set, do not store events in the database")
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

	if !*dryRun && *recreateDB {
		fmt.Println("WARNING: Database will be erased and recreated.")
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

	var events []icalplayers.Event

	if *wpURL != "" {
		// Fetch events from the WordPress tribe/events API
		events, err = wpevents.FetchAll(ctx, *wpURL)
		if err != nil {
			exitErr(fmt.Errorf("wp fetch: %w", err))
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
		parsedTeams := findTeams(ev.Description, teams)
		if len(parsedTeams) > 0 {
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

	icalplayers.SummarizeEvents(events)

	if *dryRun {
		fmt.Println("Dry run; not storing events.")
		return
	}

	if *recreateDB {
		fmt.Printf("Recreating database...\n")
		if err := store.Drop(ctx); err != nil {
			log.Fatal(err)
		}
		if err := store.Migrate(ctx); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Database recreated.\n")
	} 
	if *deletePastEvents {
		fmt.Printf("Deleting past events...\n")
		if err := store.DeletePastEvents(ctx); err != nil {
			log.Fatal(err)
		}
	}

	if *wpURL != "" {
		// Use InsertIfNew to avoid overwriting or duplicating events already imported via ICS.
		// Deduplication is by (date, summary) so collisions across different source IDs are caught.
		inserted := 0
		skipped := 0
		for _, e := range events {
			ok, err := store.InsertIfNew(ctx, e)
			if err != nil {
				exitErr(err)
			}
			if ok {
				inserted++
			} else {
				skipped++
				fmt.Printf("Skipped duplicate: %s (%s)\n", e.Summary, e.Start)
			}
		}
		fmt.Printf("Inserted %d new events, skipped %d duplicates.\n", inserted, skipped)
	} else {
		for _, e := range events {
			if err := store.Upsert(ctx, e); err != nil {
				exitErr(err)
			}
		}
		fmt.Printf("Stored %d events.\n", len(events))
	}
}

func isURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme != "" && u.Host != ""
}

func exitErr(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}

// findTeams from event description
func findTeams(desc string, teams []showstore.Team) []showstore.Team {
	var matches []showstore.Team
	for _, t := range teams {
		if len(t.Name) <= 4 { // skip short/generic names
			continue
		}
		if strings.Contains(desc, t.Name) {
			fmt.Println(t.Name)
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
