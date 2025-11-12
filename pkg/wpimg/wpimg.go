// Package wpimg downloads the first <img class="wp-post-image"> from a page.
package wpimg

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

// Result describes the saved image.
type Result struct {
	ImageURL  string // absolute image URL
	LocalPath string // file path where the image was saved
	PageURL   *url.URL
}

func Fetch(ctx context.Context, pageURL string) (Result, error) {
	var out Result

	u, err := url.Parse(pageURL)
	if err != nil {
		return out, fmt.Errorf("invalid page URL: %w", err)
	}

	out.PageURL = u

	client := &http.Client{
		Timeout: 20 * time.Second,
		// Follow redirects; default CheckRedirect is fine.
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return out, err
	}
	req.Header.Set("User-Agent", "wpimg/1.0 (+https://example.com)")

	resp, err := client.Do(req)
	if err != nil {
		return out, fmt.Errorf("get page: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return out, fmt.Errorf("get page: unexpected status %s", resp.Status)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return out, fmt.Errorf("parse HTML: %w", err)
	}

	sel := doc.Find("img.wp-post-image").First()
	if sel.Length() == 0 {
		return out, errors.New("no <img class=\"wp-post-image\"> found")
	}

	// Try common attributes in order of preference.
	imgSrc := firstNonEmptyAttr(sel, "src")
	if imgSrc == "" {
		imgSrc = firstFromSrcset(sel)
	}
	if imgSrc == "" {
		imgSrc = firstNonEmptyAttr(sel, "data-src", "data-original", "data-lazy-src")
	}
	if imgSrc == "" {
		return out, errors.New("wp-post-image has no usable src/srcset/data-src")
	}

	imgURL, err := u.Parse(imgSrc)
	if err != nil {
		return out, fmt.Errorf("resolve image URL: %w", err)
	}
	out.ImageURL = imgURL.String()
	return out, nil
}

// FetchAndSave finds the first wp-post-image on pageURL and writes it to destDir.
// Returns Result with absolute image URL and the saved file path.
func FetchAndSave(ctx context.Context, pageURL, destDir string) (Result, error) {
	out, err := Fetch(ctx, pageURL)
	if err != nil {
		return out, err
	}

	// Download image.
	imgReq, err := http.NewRequestWithContext(ctx, http.MethodGet, out.ImageURL, nil)
	if err != nil {
		return out, err
	}
	imgReq.Header.Set("User-Agent", "wpimg/1.0 (+https://example.com)")
	client := &http.Client{
		Timeout: 20 * time.Second,
		// Follow redirects; default CheckRedirect is fine.
	}
	imgResp, err := client.Do(imgReq)
	if err != nil {
		return out, fmt.Errorf("get image: %w", err)
	}
	defer imgResp.Body.Close()

	if imgResp.StatusCode < 200 || imgResp.StatusCode >= 300 {
		return out, fmt.Errorf("get image: unexpected status %s", imgResp.Status)
	}

	// Decide filename.
	filename := filenameFromHeaders(imgResp)
	if filename == "" {
		filename = path.Base(out.PageURL.Path)
	}
	filename = sanitizeFilename(filename)

	// Ensure extension. If missing, try from Content-Type.
	if !strings.Contains(filepath.Base(filename), ".") {
		ct := imgResp.Header.Get("Content-Type")
		if ext := extFromContentType(ct); ext != "" {
			filename += ext
		}
	}
	// Fallback to hash if filename empty or still looks invalid.
	if filename == "" || filename == "." || filename == string(os.PathSeparator) {
		sum := sha256.Sum256([]byte(out.ImageURL))
		filename = fmt.Sprintf("%x", sum[:8])
		if ext := extFromContentType(imgResp.Header.Get("Content-Type")); ext != "" {
			filename += ext
		}
	}

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return out, fmt.Errorf("mkdir %s: %w", destDir, err)
	}
	local := filepath.Join(destDir, filename)

	// Save to disk.
	f, err := os.Create(local)
	if err != nil {
		return out, fmt.Errorf("create file: %w", err)
	}
	defer func() {
		_ = f.Close()
	}()

	if _, err := io.Copy(f, imgResp.Body); err != nil {
		return out, fmt.Errorf("write file: %w", err)
	}

	out.LocalPath = local
	return out, nil
}

func firstNonEmptyAttr(sel *goquery.Selection, names ...string) string {
	for _, n := range names {
		if v, ok := sel.Attr(n); ok && strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func firstFromSrcset(sel *goquery.Selection) string {
	ss, ok := sel.Attr("srcset")
	if !ok || strings.TrimSpace(ss) == "" {
		return ""
	}
	// srcset: "url1 320w, url2 640w, url3 1024w"
	parts := strings.Split(ss, ",")
	if len(parts) == 0 {
		return ""
	}
	first := strings.TrimSpace(parts[0])
	// each candidate may be "url widthDescriptor"
	if sp := strings.Fields(first); len(sp) > 0 {
		return sp[0]
	}
	return ""
}

func filenameFromHeaders(resp *http.Response) string {
	cd := resp.Header.Get("Content-Disposition")
	if cd == "" {
		return ""
	}
	_, params, err := mime.ParseMediaType(cd)
	if err != nil {
		return ""
	}
	if fn, ok := params["filename*"]; ok && fn != "" {
		// RFC 5987 format: charset''urlencoded
		// keep it simple: take the substring after last '''
		if i := strings.LastIndex(fn, "''"); i >= 0 && i+2 < len(fn) {
			if dec, err := url.QueryUnescape(fn[i+2:]); err == nil {
				return dec
			}
			return fn[i+2:]
		}
		return fn
	}
	if fn, ok := params["filename"]; ok {
		return fn
	}
	return ""
}

func sanitizeFilename(name string) string {
	name = strings.TrimSpace(name)
	// Remove query fragments if they leaked into the path.
	if i := strings.IndexAny(name, "?#"); i >= 0 {
		name = name[:i]
	}
	// Strip directory traversal.
	name = filepath.Base(name)
	// Replace characters invalid on Windows and odd on Unix.
	replacer := strings.NewReplacer(
		":", "_", "*", "_", "?", "_", "\"", "_",
		"<", "_", ">", "_", "|", "_", "\\", "_", "/", "_",
	)
	return replacer.Replace(name)
}

func extFromContentType(ct string) string {
	ct = strings.TrimSpace(ct)
	if ct == "" {
		return ""
	}
	// Common image types.
	switch {
	case strings.HasPrefix(ct, "image/jpeg"):
		return ".jpg"
	case strings.HasPrefix(ct, "image/png"):
		return ".png"
	case strings.HasPrefix(ct, "image/gif"):
		return ".gif"
	case strings.HasPrefix(ct, "image/webp"):
		return ".webp"
	case strings.HasPrefix(ct, "image/avif"):
		return ".avif"
	case strings.HasPrefix(ct, "image/svg"):
		return ".svg"
	default:
		// Best effort via mime.ExtensionsByType.
		if exts, _ := mime.ExtensionsByType(ct); len(exts) > 0 {
			return exts[0]
		}
		return ""
	}
}
