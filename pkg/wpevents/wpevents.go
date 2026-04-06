// Package wpevents fetches events from The Improv Shop's WordPress tribe/events API
// and converts them to icalplayers.Event for storage.
package wpevents

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/tsny/shopsync/pkg/icalplayers"
)

// wpEvent mirrors the relevant fields from the tribe/events/v1/events API response.
type wpEvent struct {
	ID          int    `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	URL         string `json:"url"`
	StartDate   string `json:"start_date"`
	EndDate     string `json:"end_date"`
	Timezone    string `json:"timezone"`
	Image       struct {
		URL string `json:"url"`
	} `json:"image"`
}

type apiResponse struct {
	Events      []wpEvent `json:"events"`
	NextRestURL string    `json:"next_rest_url"`
	Total       int       `json:"total"`
}

var htmlTagRe = regexp.MustCompile(`<[^>]+>`)

// cdnSizeRe matches the trailing /w=NNN,h=NNN (or h=NNN,w=NNN) segment on
// Cloudflare image delivery URLs.
var cdnSizeRe = regexp.MustCompile(`/[wh]=\d+,[wh]=\d+$`)

// RewriteCdnCgiURL rewrites cdn-cgi/imagedelivery URLs so the trailing
// width/height parameters become w=512,h=512. Non-matching URLs are returned
// unchanged.
func RewriteCdnCgiURL(raw string) string {
	if !cdnSizeRe.MatchString(raw) {
		return raw
	}
	return cdnSizeRe.ReplaceAllString(raw, "/w=512,h=512")
}

func stripHTML(s string) string {
	s = htmlTagRe.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	return strings.TrimSpace(s)
}

// FetchAll fetches all pages from the given base API URL and returns converted events.
func FetchAll(ctx context.Context, baseURL string) ([]icalplayers.Event, error) {
	var all []icalplayers.Event
	nextURL := baseURL
	page := 1

	for nextURL != "" {
		fmt.Printf("Fetching WP events page %d: %s\n", page, nextURL)
		resp, err := fetchPage(ctx, nextURL)
		if err != nil {
			return nil, fmt.Errorf("page %d: %w", page, err)
		}
		for _, e := range resp.Events {
			all = append(all, convert(e))
		}
		nextURL = resp.NextRestURL
		page++
	}

	fmt.Printf("Fetched %d WP events total.\n", len(all))
	return all, nil
}

func fetchPage(ctx context.Context, url string) (*apiResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "shopsync/1.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("http status %d", resp.StatusCode)
	}

	var result apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return &result, nil
}

func convert(e wpEvent) icalplayers.Event {
	uid := fmt.Sprintf("wp-%d", e.ID)

	var start, end *time.Time
	if loc, err := time.LoadLocation(e.Timezone); err == nil {
		if t, err := time.ParseInLocation("2006-01-02 15:04:05", e.StartDate, loc); err == nil {
			start = &t
		}
		if t, err := time.ParseInLocation("2006-01-02 15:04:05", e.EndDate, loc); err == nil {
			end = &t
		}
	}

	return icalplayers.Event{
		UID:          uid,
		Summary:      html.UnescapeString(e.Title),
		Description:  stripHTML(e.Description),
		URL:          e.URL,
		PostImageURL: e.Image.URL,
		Start:        start,
		End:          end,
	}
}
