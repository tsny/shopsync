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
		fmt.Println("DRY RUN: Would move all future shows back by 1 day")
		fmt.Println("\nTo actually update, run with -dry-run=false")
	} else {
		fmt.Println("Moving all future shows back by 1 day...")
		updated, err := store.MoveFutureShowsBackOneDay(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to update dates: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Successfully updated %d shows\n", updated)
	}
}
