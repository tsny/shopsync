package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/joho/godotenv"
	"github.com/tsny/shopsync/pkg/showstore"
)

// Available image files from the Google Cloud Storage bucket
// Format: filename without extension -> full filename with extension
var imageFiles = map[string]string{
	"alteregos":      "alteregos.png",
	"asshole":        "asshole.png",
	"basereality":    "basereality.png",
	"boobs":          "boobs.jpeg",
	"cagematch":      "cagematch.png",
	"dogbody": "dogbody.png",
	"comicstrip":     "comicstrip.png",
	"free-the-8008s": "boobs.jpeg",
	"menagerie":      "menagerie.jpg",
	"grimprov":       "grimprov.jpg",
	"happyhands":     "happyhands.jpg",
	"queer-jam": "queerjam.png",
	"queer-improv-jam": "queerjam.png",
	"student-jam": "studentjam.png",
	"friday-jams": "studentjam.png",
	"sutdent-showcase": "showcase.png",
	"rhyme":"rhyme.png",
	"base-reality-check": "basereality.png",
	"you-already-know-ship-of-theseus": "yaak.png",
	"fourfives":      "fourfives.png",
	"the-one-four-fives-present-an-improvised-musical": "fourfives.png",
	"straws": "straws.png",
	"so-fake":         "sofake.png",
	"house-team-night":      "houseteam.png",
	"electric": "electric.png",
	"natural-7": "natural7.png",
	"karaokedokie": "karaokedokie.jpg",
	"student-showcases":    "studentshowcases.png",
	"ifx":            "ifx.jpg",
	"lab":            "lab.png",
	"happy-hands-social-club": "happyhands.jpg",
	"whose": "whose.png",
	"immersive-theme-park-experience-free": "leeker.png",
	"leekes-takes-you-to-an-immersive-theme-park-experience": "leeker.png",
	"leeker":         "leeker.png",
	"merry":          "merry.jpg",
	"school-night":    "schoolnight.png",
	"sketch":         "sketch.png",
	"capematch-actual-start-of-the-tournament": "cagematch.png",
	"teams-level-1":  "teams-level-1.png",
	"shirts-and-skins-with-comic-strip-and-friends": "comicstrip.png",
	"teams-level2":   "teams-level2.png",
	"titanic":        "titanic.png",
	"yaas":           "yaas.jpg",
}

const baseURL = "https://storage.googleapis.com/improv-wiki-teams/shows/res/"

// normalizeShowName converts a show name to a format that can be matched against filenames
// It converts to lowercase, removes special characters, and normalizes spaces/hyphens
func normalizeShowName(name string) string {
	// Convert to lowercase
	name = strings.ToLower(name)
	
	// Remove common prefixes/suffixes that might not be in filename
	name = strings.TrimSpace(name)
	
	// Replace spaces and underscores with hyphens
	name = strings.ReplaceAll(name, " ", "-")
	name = strings.ReplaceAll(name, "_", "-")
	
	// Remove special characters except hyphens
	reg := regexp.MustCompile(`[^a-z0-9-]`)
	name = reg.ReplaceAllString(name, "")
	
	// Remove multiple consecutive hyphens
	reg = regexp.MustCompile(`-+`)
	name = reg.ReplaceAllString(name, "-")
	
	// Remove leading/trailing hyphens
	name = strings.Trim(name, "-")
	
	return name
}

// findMatchingImage finds a matching image filename for a show name
// Returns the full filename if found, empty string otherwise
func findMatchingImage(showName string) string {
	normalized := normalizeShowName(showName)
	
	// Direct match
	if filename, ok := imageFiles[normalized]; ok {
		return filename
	}
	
	// Try exact word boundary matches first (more precise)
	// Check if normalized name contains the key as a complete word
	for key, filename := range imageFiles {
		// Match if the normalized name contains the key with word boundaries
		// e.g., "teams-level-1" matches "teams-level-1-show" or "show-teams-level-1"
		if strings.Contains(normalized, key) {
			// Verify it's a word boundary match (not just a substring)
			idx := strings.Index(normalized, key)
			// Check if it's at the start, end, or surrounded by hyphens
			if idx == 0 || idx+len(key) == len(normalized) ||
				(idx > 0 && normalized[idx-1] == '-') ||
				(idx+len(key) < len(normalized) && normalized[idx+len(key)] == '-') {
				return filename
			}
		}
	}
	
	// Try reverse: check if key contains the normalized name (for shorter show names)
	// e.g., "teams" might match "teams-level-1" if the show is just "teams"
	if len(normalized) >= 4 { // Only for reasonably long names
		for key, filename := range imageFiles {
			if strings.Contains(key, normalized) {
				// Check word boundaries
				idx := strings.Index(key, normalized)
				if idx == 0 || idx+len(normalized) == len(key) ||
					(idx > 0 && key[idx-1] == '-') ||
					(idx+len(normalized) < len(key) && key[idx+len(normalized)] == '-') {
					return filename
				}
			}
		}
	}
	
	// Try matching individual words (for compound show names)
	words := strings.Split(normalized, "-")
	for _, word := range words {
		if len(word) >= 4 { // Only match words of 4+ characters
			if filename, ok := imageFiles[word]; ok {
				return filename
			}
		}
	}
	
	return ""
}

func main() {
	dryRun := flag.Bool("dry-run", true, "If true, show what would be updated but don't actually update")
	flag.Parse()

	_ = godotenv.Load()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL not set")
		os.Exit(1)
	}

	ctx := context.Background()
	store, err := showstore.Open(ctx, dbURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect to database: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	// Get all shows without image URLs
	shows, err := store.GetShowsWithoutImageURL(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get shows: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Found %d shows without picture URLs\n\n", len(shows))

	var matched, updated int
	var noMatch []string

	for _, show := range shows {
		filename := findMatchingImage(show.Summary)
		if filename == "" {
			noMatch = append(noMatch, show.Summary)
			continue
		}

		imageURL := baseURL + filename
		fmt.Printf("Match found:\n")
		fmt.Printf("  Show: %s\n", show.Summary)
		fmt.Printf("  Image: %s\n", imageURL)
		fmt.Printf("  Normalized: %s\n", normalizeShowName(show.Summary))
		fmt.Println()

		matched++

		if !*dryRun {
			if err := store.UpdateShowImageURL(ctx, show.UID, imageURL); err != nil {
				fmt.Fprintf(os.Stderr, "Failed to update show %s: %v\n", show.Summary, err)
				continue
			}
			updated++
		}
	}

	fmt.Printf("\nSummary:\n")
	fmt.Printf("  Shows matched: %d\n", matched)
	if !*dryRun {
		fmt.Printf("  Shows updated: %d\n", updated)
	} else {
		fmt.Printf("  Would update: %d (dry-run mode)\n", matched)
	}
	fmt.Printf("  Shows without match: %d\n", len(noMatch))

	if len(noMatch) > 0 && len(noMatch) <= 20 {
		fmt.Println("\nShows without matches:")
		for _, name := range noMatch {
			fmt.Printf("  - %s (normalized: %s)\n", name, normalizeShowName(name))
		}
	} else if len(noMatch) > 20 {
		fmt.Printf("\nFirst 20 shows without matches:\n")
		for i, name := range noMatch[:20] {
			fmt.Printf("  %d. %s (normalized: %s)\n", i+1, name, normalizeShowName(name))
		}
		fmt.Printf("  ... and %d more\n", len(noMatch)-20)
	}
}
