package icalplayers

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	ics "github.com/arran4/golang-ical"
	"github.com/tsny/shopsync/pkg/wpimg"
)

var SkipImageSearch = false

type Event struct {
	UID          string     `json:"uid"`
	Summary      string     `json:"summary"`
	Description  string     `json:"description"`
	Location     string     `json:"location"`
	URL          string     `json:"url"`
	PostImageURL string     `json:"postImageUrl,omitempty"`
	Organizer    string     `json:"organizer"`
	Start        *time.Time `json:"start,omitempty"`
	End          *time.Time `json:"end,omitempty"`
	AllDay       bool       `json:"allDay"`
	Players      []string   `json:"players,omitempty"`
	Teams        []string   `json:"teams,omitempty"`
	TeamIDs      []string   `json:"teamIds,omitempty"`
}

type NameDict struct {
	First map[string]struct{}
	Last  map[string]struct{}
	Full  map[string]struct{}
}

func LoadNameDict(csvPath string) (*NameDict, error) {
	nd := &NameDict{
		First: map[string]struct{}{},
		Last:  map[string]struct{}{},
		Full:  map[string]struct{}{},
	}
	if csvPath == "" {
		return nd, nil
	}
	f, err := os.Open(csvPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(bufio.NewReader(f))
	r.TrimLeadingSpace = true
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		for i := range rec {
			rec[i] = strings.TrimSpace(rec[i])
		}
		if len(rec) > 0 && rec[0] != "" {
			nd.First[strings.ToLower(rec[0])] = struct{}{}
		}
		if len(rec) > 1 && rec[1] != "" {
			nd.Last[strings.ToLower(rec[1])] = struct{}{}
		}
		if len(rec) > 2 && rec[2] != "" {
			nd.Full[strings.ToLower(rec[2])] = struct{}{}
		}
	}
	return nd, nil
}

// Top-level helpers

func FromReader(r io.Reader, dict *NameDict) ([]Event, error) {
	cal, err := ics.ParseCalendar(r)
	if err != nil {
		return nil, fmt.Errorf("parse ics: %w", err)
	}
	evs := collectEvents(cal)
	for i := range evs {
		evs[i].Players = InferPlayerNames(evs[i].Description, dict)
		if !SkipImageSearch {
			postResult, _ := wpimg.Fetch(context.Background(), evs[i].URL)
			if postResult.ImageURL != "" {
				evs[i].PostImageURL = postResult.ImageURL
				fmt.Println("Fetched post image:", postResult.ImageURL)
			}
		}
	}
	return evs, nil
}

func FromFile(path string, dict *NameDict) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return FromReader(f, dict)
}

func FromURL(ctx context.Context, raw string, client *http.Client, dict *NameDict) ([]Event, error) {
	if client == nil {
		client = http.DefaultClient
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, errors.New("invalid url")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "icalplayers/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("http status %d", resp.StatusCode)
	}
	return FromReader(resp.Body, dict)
}

func JSON(evs []Event) []byte {
	b, _ := json.MarshalIndent(evs, "", "  ")
	return b
}

// Internal: basic VEVENT projection

func collectEvents(cal *ics.Calendar) []Event {
	var out []Event
	for _, ve := range cal.Events() {
		ev := Event{
			UID:         propVal(ve, ics.ComponentPropertyUniqueId),
			Summary:     propVal(ve, ics.ComponentPropertySummary),
			Description: propVal(ve, ics.ComponentPropertyDescription),
			Location:    propVal(ve, ics.ComponentPropertyLocation),
			Organizer:   propVal(ve, ics.ComponentPropertyOrganizer),
			URL:         propVal(ve, ics.ComponentPropertyUrl),
			AllDay:      isAllDay(ve),
		}
		if t, err := ve.GetStartAt(); err == nil {
			ev.Start = &t
		}
		if t, err := ve.GetEndAt(); err == nil {
			ev.End = &t
		}
		out = append(out, ev)
	}
	return out
}

func propVal(ve *ics.VEvent, key ics.ComponentProperty) string {
	if p := ve.GetProperty(key); p != nil {
		return p.Value
	}
	return ""
}

func isAllDay(ve *ics.VEvent) bool {
	p := ve.GetProperty(ics.ComponentPropertyDtStart)
	if p == nil {
		return false
	}
	return false
	// return strings.EqualFold(p.ICalParameters.Get("VALUE"), "DATE")
}

// ---------- Player inference (updated) ----------

var (
	// Cue lines like “Cast: …”, “Hosted by: A and B”, “Special Guests: …”
	cueLine = regexp.MustCompile(`(?i)^(players?|cast|featuring|with|lineup|performers?|host(?:ed)?\s*by|guests?|special\s+guests?|musical\s+guest)\s*[:\-]\s*(.+)$`)
	sepRe   = regexp.MustCompile(`\s*(?:,|&| and |;|\+)\s*`)

	// Phrases that indicate non-player roles or team/group names
	stopPhrases = map[string]struct{}{
		"doors open":        {},
		"general admission": {},
		"improv jam":        {},
		"open mic":          {},
		"musical guest":     {},
		"guest team":        {},
		"on tech":           {},
		"improv from":       {},
		"vs":                {},
		"vs.":               {},
		"team":              {},
	}

	// Single title-case words commonly capitalized but not person names
	stopSingles = map[string]struct{}{
		"Show": {}, "Jam": {}, "Night": {}, "The": {}, "Team": {}, "House": {},
		"Doors": {}, "Open": {}, "Admission": {}, "Free": {}, "Improv": {},
		"Guest": {}, "Guests": {}, "Special": {}, "Musical": {}, "Featuring": {},
	}
)

// InferPlayerNames extracts plausible player names from DESCRIPTION.
// dict is optional but boosts precision.
func InferPlayerNames(desc string, dict *NameDict) []string {
	desc = strings.ReplaceAll(desc, "\r\n", "\n")
	lines := strings.Split(desc, "\n")
	var candidates []string

	// 1) Cue lines
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		if m := cueLine.FindStringSubmatch(ln); m != nil {
			role := strings.ToLower(strings.TrimSpace(m[1]))
			values := m[2]
			parts := sepRe.Split(values, -1)
			for _, p := range parts {
				if n := cleanName(p); n != "" && !containsStopContext(ln) {
					// Treat “hosted by” and “musical guest” as non-players by default.
					if strings.Contains(role, "host") || strings.Contains(role, "musical") {
						continue
					}
					candidates = append(candidates, n)
				}
			}
		}
	}

	// 2) Title-Case chunking if nothing direct
	if len(candidates) == 0 {
		for _, chunk := range titleCaseChunks(desc) {
			if isStopPhrase(chunk) {
				continue
			}
			if acceptByDict(chunk, dict) {
				candidates = append(candidates, chunk)
			}
		}
	}

	// 3) If still empty, allow single tokens from dict.First
	if len(candidates) == 0 && dict != nil && len(dict.First) > 0 {
		for _, tok := range singleTitleTokens(desc) {
			if _, ok := dict.First[strings.ToLower(tok)]; ok && !isStopSingle(tok) {
				candidates = append(candidates, tok)
			}
		}
	}

	return normalizeAndDedup(candidates)
}

func containsStopContext(line string) bool {
	l := strings.ToLower(line)
	for k := range stopPhrases {
		if strings.Contains(l, k) {
			return true
		}
	}
	return false
}

func cleanName(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "([{-"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	s = strings.Trim(s, `"'`)
	s = strings.Join(strings.Fields(s), " ")
	parts := strings.Split(s, " ")
	if len(parts) == 0 || len(parts) > 3 {
		return ""
	}
	okParts := 0
	for _, p := range parts {
		if looksLikeNameToken(p) {
			okParts++
		}
	}
	if okParts == len(parts) {
		return s
	}
	return ""
}

func looksLikeNameToken(tok string) bool {
	if tok == "" {
		return false
	}
	// Initials like "J." or "JR"
	if len(tok) <= 3 && allLettersDot(tok) && strings.ToUpper(tok) == tok {
		return true
	}
	// Title-Case including unicode like O’Nay
	rs := []rune(tok)
	return unicode.IsUpper(rs[0])
}

func allLettersDot(s string) bool {
	for _, r := range s {
		if !unicode.IsLetter(r) && r != '.' && r != '’' && r != '\'' {
			return false
		}
	}
	return true
}

func titleCaseChunks(text string) []string {
	words := strings.FieldsFunc(text, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune(",;:!?.()[]{}|/\\+-–—", r)
	})
	var chunks []string
	var buf []string
	flush := func() {
		if len(buf) >= 1 && len(buf) <= 3 {
			chunks = append(chunks, strings.Join(buf, " "))
		}
		buf = buf[:0]
	}
	for _, w := range words {
		w = strings.Trim(w, `"'`)
		if w == "" {
			continue
		}
		if looksLikeNameToken(w) && !isStopSingle(w) {
			buf = append(buf, w)
		} else {
			flush()
		}
	}
	flush()
	// Prefer multi-word first
	slices.SortFunc(chunks, func(a, b string) int {
		return len(strings.Fields(b)) - len(strings.Fields(a))
	})
	return chunks
}

func singleTitleTokens(text string) []string {
	words := strings.FieldsFunc(text, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune(",;:!?.()[]{}|/\\+-–—", r)
	})
	var out []string
	for _, w := range words {
		w = strings.Trim(w, `"'`)
		if looksLikeNameToken(w) && !isStopSingle(w) {
			out = append(out, w)
		}
	}
	return out
}

func isStopPhrase(s string) bool {
	ls := strings.ToLower(strings.TrimSpace(s))
	if _, ok := stopPhrases[ls]; ok {
		return true
	}
	parts := strings.Fields(ls)
	if len(parts) == 2 {
		if _, ok := stopSingles[strings.Title(parts[0])]; ok {
			return true
		}
		if _, ok := stopSingles[strings.Title(parts[1])]; ok {
			return true
		}
	}
	return false
}

func isStopSingle(s string) bool {
	_, ok := stopSingles[s]
	return ok
}

func acceptByDict(chunk string, dict *NameDict) bool {
	if dict == nil {
		return len(strings.Fields(chunk)) >= 2
	}
	lc := strings.ToLower(chunk)
	if _, ok := dict.Full[lc]; ok {
		return true
	}
	parts := strings.Fields(lc)
	switch len(parts) {
	case 1:
		_, f := dict.First[parts[0]]
		return f
	case 2:
		_, f := dict.First[parts[0]]
		_, l := dict.Last[parts[1]]
		return f || l
	case 3:
		_, f := dict.First[parts[0]]
		_, l := dict.Last[parts[2]]
		return f || l
	default:
		return false
	}
}

func normalizeAndDedup(in []string) []string {
	seen := map[string]struct{}{}
	best := map[string]string{} // longest per first token
	for _, s := range in {
		sn := strings.Join(strings.Fields(s), " ")
		if sn == "" {
			continue
		}
		base := strings.ToLower(strings.Fields(sn)[0])
		cur, ok := best[base]
		if !ok || len(sn) > len(cur) {
			best[base] = sn
		}
	}
	out := make([]string, 0, len(best))
	for _, v := range best {
		l := strings.ToLower(v)
		if _, ok := seen[l]; !ok {
			seen[l] = struct{}{}
			out = append(out, v)
		}
	}
	slices.Sort(out)
	return out
}

func SummarizeEvents(events []Event) {
	// Text output
	if len(events) == 0 {
		fmt.Println("No VEVENTs found.")
		return
	}
	for _, ev := range events {
		fmt.Printf("UID:         %s\n", ev.UID)
		fmt.Printf("Summary:     %s\n", ev.Summary)
		fmt.Printf("URL:        %s\n", ev.URL)
		if ev.Start != nil {
			fmt.Printf("Start:       %s\n", ev.Start.Format(time.RFC3339))
		}
		// fmt.Printf("Players:   %v\n", ev.Players)
		fmt.Printf("Description:\n%s\n", coalesce(ev.Description, "(none)"))
		fmt.Printf("Teams:     %v\n", ev.Teams)
		fmt.Printf("Team IDs:  %v\n", ev.TeamIDs)
		fmt.Println(strings.Repeat("-", 60))
	}
}
func coalesce(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	if len(s) > 50 {
		return s[:50] + "..."
	}
	return s
}
