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

	"github.com/joho/godotenv"
	"github.com/tsny/shopsync/pkg/icalplayers"
	"github.com/tsny/shopsync/pkg/showstore"
	"github.com/tsny/shopsync/pkg/wpimg"
)

func main() {
	src := flag.String("src", "", "Path or URL to an .ics file. Use '-' to read from stdin")
	postURL := flag.String("post-url", "", "testing param: grabs image from given post URL")
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

	if *src == "" {
		exitErr(errors.New("missing -src"))
	}

	var events []icalplayers.Event
	isUrl := isURL(*src)
	if isUrl {
		fmt.Printf("Reading ICS from URL: %s\n", *src)
		events, err = icalplayers.FromURL(context.Background(), *src, http.DefaultClient, nil)
		if err != nil {
			exitErr(err)
		}
	} else {
		fmt.Printf("Reading ICS from file: %s\n", *src)
		events, err = icalplayers.FromFile(*src, nil)
		if err != nil {
			exitErr(err)
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
	} else {
		fmt.Printf("Deleting past events...\n")
		if err := store.DeletePastEvents(ctx); err != nil {
			log.Fatal(err)
		}
	}

	for _, e := range events {
		if err := store.Upsert(ctx, e); err != nil {
			exitErr(err)
		}
	}
	fmt.Printf("Stored %d events.\n", len(events))
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
