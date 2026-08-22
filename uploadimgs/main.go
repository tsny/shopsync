// uploadimgs downloads external show images, converts them to WebP, uploads
// them to GCS, and updates the DB with the new URL.
//
// Requires Application Default Credentials with write access to the GCS bucket.
// The bucket must be configured to allow public read (allUsers Storage Object Viewer).
//
// Usage:
//
//	go run ./uploadimgs/ [-dry-run=false] [-bucket=improv-wiki-teams] [-prefix=shows/res/]
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"cloud.google.com/go/storage"
	"github.com/joho/godotenv"
	"github.com/tsny/shopsync/pkg/imgproc"
	"github.com/tsny/shopsync/pkg/showstore"
)

const (
	defaultBucket = "improv-wiki-teams"
	defaultPrefix = "shows/res/"
)

func main() {
	dryRun := flag.Bool("dry-run", true, "Show what would be done without uploading or updating the DB")
	bucket := flag.String("bucket", defaultBucket, "GCS bucket name")
	prefix := flag.String("prefix", defaultPrefix, "GCS object prefix for uploaded images")
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
		fmt.Fprintf(os.Stderr, "connect db: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	var gcs *storage.Client
	if !*dryRun {
		gcs, err = storage.NewClient(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "gcs client: %v\n", err)
			os.Exit(1)
		}
		defer gcs.Close()
	}

	shows, err := store.GetShowsWithExternalImageURL(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "query: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Found %d shows with external image URLs\n\n", len(shows))

	var processed, failed int
	for _, show := range shows {
		if show.PostImageURL == nil {
			continue
		}
		srcURL := *show.PostImageURL
		objectName := *prefix + show.UID + ".webp"
		gcsURL := fmt.Sprintf("https://storage.googleapis.com/%s/%s", *bucket, objectName)

		fmt.Printf("Show:   %s\n", show.Summary)
		fmt.Printf("  From: %s\n", srcURL)
		fmt.Printf("  To:   %s\n", gcsURL)

		if *dryRun {
			fmt.Println("  (dry-run, skipping)")
			fmt.Println()
			continue
		}

		newURL, err := imgproc.ConvertAndUpload(ctx, gcs, srcURL, *bucket, objectName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  upload error: %v\n", err)
			failed++
			fmt.Println()
			continue
		}

		if err := store.UpdateShowImageURL(ctx, show.UID, newURL); err != nil {
			fmt.Fprintf(os.Stderr, "  db update error: %v\n", err)
			failed++
			fmt.Println()
			continue
		}

		fmt.Printf("  Uploaded and updated DB\n")
		processed++
		fmt.Println()
	}

	fmt.Printf("Summary: %d uploaded, %d failed, %d total\n", processed, failed, len(shows))
}
