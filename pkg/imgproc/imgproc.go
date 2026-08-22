// Package imgproc downloads, resizes, and re-encodes images as WebP.
package imgproc

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"github.com/chai2010/webp"
	"github.com/disintegration/imaging"
)

const (
	MaxWidth = 800
	Quality  = 85
)

// IsGCSURL reports whether url is already stored in the given GCS bucket.
func IsGCSURL(url, bucket string) bool {
	return strings.HasPrefix(url, "https://storage.googleapis.com/"+bucket+"/")
}

// ConvertAndUpload converts the image at imageURL to WebP and uploads it to
// gs://bucket/object. Returns the public HTTPS URL.
func ConvertAndUpload(ctx context.Context, gcs *storage.Client, imageURL, bucket, object string) (string, error) {
	data, err := Convert(ctx, imageURL)
	if err != nil {
		return "", err
	}

	obj := gcs.Bucket(bucket).Object(object)
	w := obj.NewWriter(ctx)
	w.ContentType = "image/webp"
	w.CacheControl = "public, max-age=31536000"
	if _, err := w.Write(data); err != nil {
		_ = w.Close()
		return "", fmt.Errorf("upload write: %w", err)
	}
	if err := w.Close(); err != nil {
		return "", fmt.Errorf("upload close: %w", err)
	}

	// Best-effort; silently ignored when bucket uses uniform IAM access.
	_ = obj.ACL().Set(ctx, storage.AllUsers, storage.RoleReader)

	return fmt.Sprintf("https://storage.googleapis.com/%s/%s", bucket, object), nil
}

// Convert downloads the image at imageURL, resizes it so its width is at most
// MaxWidth pixels (preserving aspect ratio), and returns the bytes encoded as
// WebP at Quality.
func Convert(ctx context.Context, imageURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "shopsync-imgproc/1.0")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("download: status %s", resp.Status)
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("decode image: %w", err)
	}

	if img.Bounds().Dx() > MaxWidth {
		img = imaging.Resize(img, MaxWidth, 0, imaging.Lanczos)
	}

	var buf bytes.Buffer
	if err := webp.Encode(&buf, img, &webp.Options{Quality: Quality}); err != nil {
		return nil, fmt.Errorf("encode webp: %w", err)
	}
	return buf.Bytes(), nil
}
