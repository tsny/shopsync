// resizeimgs rewrites cdn-cgi/imagedelivery URLs in the shows table so that
// the trailing width/height parameters are normalised to w=512,h=512.
//
// Example transformation:
//
//	.../house-team-night.jpg/w=1080,h=696  ->  .../house-team-night.jpg/w=512,h=512
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"regexp"

	"github.com/joho/godotenv"
	"github.com/tsny/shopsync/pkg/showstore"
)

// cdnSizeRe matches the trailing /w=NNN,h=NNN (or h=NNN,w=NNN) segment.
var cdnSizeRe = regexp.MustCompile(`/[wh]=\d+,[wh]=\d+$`)

func rewriteURL(raw string) (string, bool) {
	if !cdnSizeRe.MatchString(raw) {
		return raw, false
	}
	rewritten := cdnSizeRe.ReplaceAllString(raw, "/w=512,h=512")
	return rewritten, rewritten != raw
}

func main() {
	dryRun := flag.Bool("dry-run", true, "Show what would be updated without writing to the DB")
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
		fmt.Fprintf(os.Stderr, "connect: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	shows, err := store.GetShowsWithCdnCgiURL(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "query: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Found %d shows with cdn-cgi image URLs\n\n", len(shows))

	var updated, skipped int
	for _, show := range shows {
		if show.PostImageURL == nil {
			continue
		}
		newURL, changed := rewriteURL(*show.PostImageURL)
		if !changed {
			skipped++
			continue
		}

		fmt.Printf("Show:    %s\n", show.Summary)
		fmt.Printf("  Before: %s\n", *show.PostImageURL)
		fmt.Printf("  After:  %s\n", newURL)

		if !*dryRun {
			if err := store.UpdateShowImageURL(ctx, show.UID, newURL); err != nil {
				fmt.Fprintf(os.Stderr, "  ERROR updating %s: %v\n", show.UID, err)
				continue
			}
		}
		updated++
	}

	fmt.Printf("\nSummary:\n")
	if *dryRun {
		fmt.Printf("  Would update: %d\n", updated)
	} else {
		fmt.Printf("  Updated: %d\n", updated)
	}
	fmt.Printf("  Already correct (no change): %d\n", skipped)
}
