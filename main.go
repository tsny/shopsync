// main.go
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"

	"github.com/joho/godotenv"
	"github.com/tsny/shopsync/pkg/icalplayers"
	"github.com/tsny/shopsync/pkg/showstore"
)

func main() {
	src := flag.String("src", "", "Path or URL to an .ics file. Use '-' to read from stdin")
	// jsonOut := flag.Bool("json", false, "Output JSON instead of text")
	// timeout := flag.Duration("timeout", 15*time.Second, "Download timeout")
	flag.Parse()

	_ = godotenv.Load()

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

	if err := store.Drop(ctx); err != nil {
		log.Fatal(err)
	}
	if err := store.Migrate(ctx); err != nil {
		log.Fatal(err)
	}

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
	icalplayers.SummarizeEvents(events)

	if len(events) == 0 {
		fmt.Println("No events to store.")
		return
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
