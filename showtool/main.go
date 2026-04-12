package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/tsny/shopsync/pkg/icalplayers"
	"github.com/tsny/shopsync/pkg/showstore"
)

// Event matches icalplayers.Event for DB insertion
type Event struct {
	UID          string
	Summary      string
	Description  string
	Location     string
	URL          string
	PostImageURL string
	Start        *time.Time
	End          *time.Time
	Players      []string
	Teams        []string
	TeamIDs      []string
}

// TSVRow represents a raw parsed row
type TSVRow struct {
	DateRaw     string
	TimeRaw     string
	Venue       string
	ShowDetails string
	TeamsRaw    string
}

// Date formats found in the file
var dateFormats = []string{
	"Monday, January 2", // Thursday, January 2nd
	"Mon, Jan 2",        // Thurs, Jan 23rd
	"January 2",         // July 10th
}

var ordinalRegex = regexp.MustCompile(`(\d+)(st|nd|rd|th)`)

var timeFormats = []string{
	"3:04 PM",
	"3:04PM",
	"3:04",
	"15:04",
}

func parseDate(s string, year int) (time.Time, string, error) {
	// Handle "July 10th @ 7:00" format - split on @
	timeFromDate := ""
	if idx := strings.Index(s, "@"); idx != -1 {
		timeFromDate = strings.TrimSpace(s[idx+1:])
		s = strings.TrimSpace(s[:idx])
	}

	// Remove ordinal suffixes: 1st -> 1, 2nd -> 2, etc.
	s = ordinalRegex.ReplaceAllString(s, "$1")

	// Normalize weekday abbreviations (careful not to mangle Thursday -> Thuday)
	s = strings.Replace(s, "Thurs,", "Thu,", 1)
	s = strings.Replace(s, "Tues,", "Tue,", 1)

	// Try each format
	for _, format := range dateFormats {
		if t, err := time.Parse(format, s); err == nil {
			return time.Date(year, t.Month(), t.Day(), 0, 0, 0, 0, time.Local), timeFromDate, nil
		}
	}

	return time.Time{}, timeFromDate, fmt.Errorf("unable to parse date: %s", s)
}

func parseTime(s string) (hour, min int, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" || s == "N/A" || strings.Contains(s, "Not") {
		return 0, 0, false
	}

	// Handle ranges like "6:00-7:15 PM" - take the start time
	if idx := strings.Index(s, "-"); idx != -1 {
		s = strings.TrimSpace(s[:idx])
	}

	// Try parsing with various formats
	s = strings.ToUpper(s)
	for _, format := range timeFormats {
		if t, err := time.Parse(format, s); err == nil {
			return t.Hour(), t.Minute(), true
		}
	}

	return 0, 0, false
}

func parseTeams(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" || s == "[Not Specified]" || s == "Not specified" || s == "Not specified in details" || s == "N/A" {
		return nil
	}

	// Common separators: ", ", " and ", " plus ", "; "
	// Replace " and " with comma for splitting
	s = strings.ReplaceAll(s, ", and ", ", ")
	s = strings.ReplaceAll(s, " and ", ", ")
	s = strings.ReplaceAll(s, " plus ", ", ")
	s = strings.ReplaceAll(s, "; ", ", ")

	parts := strings.Split(s, ", ")
	var teams []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" && p != "N/A" {
			teams = append(teams, p)
		}
	}
	return teams
}

func generateUID(date time.Time, summary, venue string, lineNum int) string {
	// Include time, venue, and line number to ensure uniqueness
	h := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%d", date.Format("2006-01-02 15:04"), summary, venue, lineNum)))
	return hex.EncodeToString(h[:8]) // 16 char hex
}

func rowToEvent(row TSVRow, year, lineNum int) (Event, error) {
	date, timeFromDate, err := parseDate(row.DateRaw, year)
	if err != nil {
		return Event{}, err
	}

	// Merge time into date
	timeStr := row.TimeRaw
	if timeFromDate != "" {
		timeStr = timeFromDate
	}
	if hour, min, ok := parseTime(timeStr); ok {
		date = time.Date(date.Year(), date.Month(), date.Day(), hour, min, 0, 0, time.Local)
	}

	teams := parseTeams(row.TeamsRaw)

	return Event{
		UID:         generateUID(date, row.ShowDetails, row.Venue, lineNum),
		Summary:     row.ShowDetails,
		Description: row.TeamsRaw, // Use teams as description
		Location:    row.Venue,
		Start:       &date,
		Teams:       teams,
	}, nil
}

// matchTeams finds DB teams that appear in the event's teams or description
func matchTeams(event *Event, dbTeams []showstore.Team) {
	var matchedTeams []string
	var matchedIDs []string

	for _, dbTeam := range dbTeams {
		if len(dbTeam.Name) <= 4 { // skip short/generic names
			continue
		}

		// Check if DB team name appears in parsed teams
		for _, parsedTeam := range event.Teams {
			if strings.Contains(parsedTeam, dbTeam.Name) {
				matchedTeams = append(matchedTeams, dbTeam.Name)
				matchedIDs = append(matchedIDs, dbTeam.ID)
				break
			}
		}

		// Also check description (raw teams text)
		if strings.Contains(event.Description, dbTeam.Name) {
			// Avoid duplicates
			found := false
			for _, t := range matchedTeams {
				if t == dbTeam.Name {
					found = true
					break
				}
			}
			if !found {
				matchedTeams = append(matchedTeams, dbTeam.Name)
				matchedIDs = append(matchedIDs, dbTeam.ID)
			}
		}
	}

	event.Teams = matchedTeams
	event.TeamIDs = matchedIDs
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func teamsEqual(a, b []string) bool {
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

// toIcalEvent converts local Event to icalplayers.Event for DB insertion
func toIcalEvent(e Event) icalplayers.Event {
	return icalplayers.Event{
		UID:         e.UID,
		Summary:     e.Summary,
		Description: e.Description,
		Location:    e.Location,
		URL:         e.URL,
		PostImageURL: e.PostImageURL,
		Start:       e.Start,
		End:         e.End,
		Players:     e.Players,
		Teams:       e.Teams,
		TeamIDs:     e.TeamIDs,
	}
}

func main() {
	insertDB := flag.Bool("insert", false, "Insert new shows into database (skips existing)")
	dryRun := flag.Bool("dry-run", true, "If true, show what would be inserted but don't actually insert")
	flag.Parse()

	_ = godotenv.Load("../.env") // Load from parent directory

	// Connect to database
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		fmt.Println("DATABASE_URL not set, running without team matching or DB insertion")
	}

	ctx := context.Background()
	var store *showstore.Store
	var dbTeams []showstore.Team

	if dbURL != "" {
		var err error
		store, err = showstore.Open(ctx, dbURL)
		if err != nil {
			fmt.Printf("Failed to connect to DB: %v\n", err)
		} else {
			defer store.Close()
			dbTeams, err = store.GetAllTeams(ctx)
			if err != nil {
				fmt.Printf("Failed to get teams: %v\n", err)
			} else {
				fmt.Printf("Loaded %d teams from database\n", len(dbTeams))
			}
		}
	}

	file, err := os.Open("IS SHOWS 2025 - Untitled.tsv")
	if err != nil {
		panic(err)
	}
	defer file.Close()

	var events []Event
	var lastDateRaw string
	var parseErrors int
	lineNum := 1 // start at 1, header is line 1

	scanner := bufio.NewScanner(file)
	scanner.Scan() // skip header

	for scanner.Scan() {
		lineNum++
		fields := strings.Split(scanner.Text(), "\t")
		if len(fields) < 4 {
			continue
		}

		dateRaw := strings.TrimSpace(fields[0])
		if dateRaw == "" {
			dateRaw = lastDateRaw
		} else {
			lastDateRaw = dateRaw
		}

		var teamsRaw string
		if len(fields) >= 5 {
			teamsRaw = strings.TrimSpace(fields[4])
		}

		row := TSVRow{
			DateRaw:     dateRaw,
			TimeRaw:     strings.TrimSpace(fields[1]),
			Venue:       strings.TrimSpace(fields[2]),
			ShowDetails: strings.TrimSpace(fields[3]),
			TeamsRaw:    teamsRaw,
		}

		event, err := rowToEvent(row, 2025, lineNum)
		if err != nil {
			parseErrors++
			continue
		}

		// Match teams against database
		if len(dbTeams) > 0 {
			matchTeams(&event, dbTeams)
		}

		events = append(events, event)
	}

	fmt.Printf("\nConverted %d rows to Events (%d errors)\n\n", len(events), parseErrors)

	// Show sample events with matched teams
	fmt.Println("Sample Events with Matched Teams:")
	shown := 0
	for _, e := range events {
		if len(e.Teams) > 0 && shown < 10 {
			fmt.Printf("  %s | %s\n", e.Start.Format("2006-01-02"), e.Summary)
			fmt.Printf("    Teams: %v\n", e.Teams)
			fmt.Printf("    IDs:   %v\n\n", e.TeamIDs)
			shown++
		}
	}

	// Stats
	var withMatchedTeams int
	totalMatches := 0
	for _, e := range events {
		if len(e.Teams) > 0 {
			withMatchedTeams++
			totalMatches += len(e.Teams)
		}
	}

	fmt.Println("Team Matching Stats:")
	fmt.Printf("  Events with matched teams: %d/%d (%.0f%%)\n", withMatchedTeams, len(events), float64(withMatchedTeams)/float64(len(events))*100)
	fmt.Printf("  Total team matches: %d\n", totalMatches)

	// Write output TSV
	outFile, err := os.Create("shows_parsed.tsv")
	if err != nil {
		panic(err)
	}
	defer outFile.Close()

	// Header
	fmt.Fprintln(outFile, "UID\tDate\tTime\tSummary\tLocation\tTeams\tTeamIDs\tDescription")

	for _, e := range events {
		dateStr := ""
		timeStr := ""
		if e.Start != nil {
			dateStr = e.Start.Format("2006-01-02")
			timeStr = e.Start.Format("15:04")
		}

		teams := strings.Join(e.Teams, ", ")
		teamIDs := strings.Join(e.TeamIDs, ", ")

		// Escape tabs/newlines in description
		desc := strings.ReplaceAll(e.Description, "\t", " ")
		desc = strings.ReplaceAll(desc, "\n", " ")

		fmt.Fprintf(outFile, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			e.UID, dateStr, timeStr, e.Summary, e.Location, teams, teamIDs, desc)
	}

	fmt.Printf("\nWrote %d events to shows_parsed.tsv\n", len(events))

	// Insert into database if requested
	if *insertDB && store != nil {
		fmt.Println("\n--- Database Insertion ---")

		if *dryRun {
			fmt.Println("DRY RUN: Checking which shows would be inserted or updated...")
		}

		var inserted, updated, skipped, errs int

		for _, e := range events {
			icalEvent := toIcalEvent(e)

			existing, err := store.FindByDateAndSummary(ctx, e.Start, e.Summary)
			if err != nil {
				fmt.Printf("  ERROR looking up %s: %v\n", e.Summary, err)
				errs++
				continue
			}

			if existing == nil {
				// New show
				if *dryRun {
					inserted++
					if inserted <= 10 {
						fmt.Printf("  WOULD INSERT: %s | %s\n", e.Start.Format("2006-01-02"), e.Summary)
					}
				} else {
					wasInserted, err := store.InsertIfNew(ctx, icalEvent)
					if err != nil {
						fmt.Printf("  ERROR inserting %s: %v\n", e.Summary, err)
						errs++
						continue
					}
					if wasInserted {
						inserted++
						fmt.Printf("  INSERTED: %s | %s\n", e.Start.Format("2006-01-02"), e.Summary)
					}
				}
			} else {
				// Existing show — check if description or teams changed
				descChanged := existing.Description != icalEvent.Description
				teamsChanged := !teamsEqual(existing.Teams, icalEvent.Teams)
				if !descChanged && !teamsChanged {
					skipped++
					fmt.Printf("  UNCHANGED: %s | %s\n", e.Start.Format("2006-01-02"), e.Summary)
					continue
				}
				verb := map[bool]string{true: "WOULD UPDATE", false: "UPDATED"}[*dryRun]
				fmt.Printf("  %s: %s | %s\n", verb, e.Start.Format("2006-01-02"), e.Summary)
				if descChanged {
					fmt.Printf("    description: %q\n              -> %q\n",
						truncate(existing.Description, 80), truncate(icalEvent.Description, 80))
				}
				if teamsChanged {
					fmt.Printf("    teams: %v -> %v\n", existing.Teams, icalEvent.Teams)
				}
				if !*dryRun {
					if err := store.UpdateDescriptionAndTeams(ctx, existing.UID, icalEvent.Description, icalEvent.Teams, icalEvent.TeamIDs); err != nil {
						fmt.Printf("    ERROR: %v\n", err)
						errs++
						continue
					}
				}
				updated++
			}
		}

		verb := map[bool]string{true: "Would insert", false: "Inserted"}[*dryRun]
		verbU := map[bool]string{true: "Would update", false: "Updated"}[*dryRun]
		fmt.Printf("\nResults:\n")
		fmt.Printf("  %s: %d\n", verb, inserted)
		fmt.Printf("  %s: %d\n", verbU, updated)
		fmt.Printf("  Unchanged: %d\n", skipped)
		if errs > 0 {
			fmt.Printf("  Errors: %d\n", errs)
		}
	}
}
