package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/joho/godotenv"
	"github.com/tsny/shopsync/pkg/showstore"
)

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

	if *dryRun {
		fmt.Println("DRY RUN: Would update all AM times to PM")
		fmt.Println("(Times already in PM will remain unchanged)")
		fmt.Println("\nTo actually update, run with -dry-run=false")
	} else {
		fmt.Println("Updating all show times to PM...")
		
		// First update: convert AM to PM
		updated, err := store.UpdateAllTimesToPM(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to update times: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("First pass: Updated %d shows from AM to PM\n", updated)
		
		// Second update: catch any remaining AM times (in case of timezone issues)
		updated2, err := store.UpdateAllTimesToPM(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed on second pass: %v\n", err)
			os.Exit(1)
		}
		if updated2 > 0 {
			fmt.Printf("Second pass: Updated %d more shows\n", updated2)
		}
		
		fmt.Printf("Total: Successfully updated %d shows\n", updated+updated2)
	}
}
