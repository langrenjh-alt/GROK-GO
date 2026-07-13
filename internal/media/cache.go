package media

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

// RemoteCache downloads public upstream media through an SSRF-hardened client,
// stores it locally, and returns a short-lived signed URL.
type RemoteCache struct {
	Store         Store
	Signer        *Signer
	PublicBaseURL string
	Client        *http.Client
	Policy        SSRFPolicy
	MaxFetchBytes int64
	ImageTTL      time.Duration
	VideoTTL      time.Duration
}

func (c *RemoteCache) Localize(ctx context.Context, kind, rawURL string) (string, error) {
	if c == nil || c.Store == nil || c.Signer == nil {
		return "", errors.New("remote media cache is not configured")
	}
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind != "image" && kind != "video" {
		return "", errors.New("remote media kind must be image or video")
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(rawURL)), "data:") {
		return c.localizeDataURL(ctx, kind, rawURL)
	}
	parsed, err := ValidateURL(ctx, rawURL, c.Policy)
	if err != nil {
		return "", err
	}
	client := c.Client
	if client == nil {
		client = NewSafeClient(nil, c.Policy)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Accept", kind+"/*")
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("fetch remote media: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("fetch remote media: HTTP %d", response.StatusCode)
	}
	maximum := c.MaxFetchBytes
	if maximum <= 0 {
		maximum = 25 << 20
	}
	if response.ContentLength > maximum {
		return "", ErrMediaTooLarge
	}
	reader := bufio.NewReader(response.Body)
	contentType := response.Header.Get("Content-Type")
	if base, _, parseErr := mime.ParseMediaType(contentType); parseErr == nil {
		contentType = base
	}
	if !strings.HasPrefix(strings.ToLower(contentType), kind+"/") {
		sample, _ := reader.Peek(512)
		contentType = http.DetectContentType(sample)
	}
	if !strings.HasPrefix(strings.ToLower(contentType), kind+"/") {
		return "", fmt.Errorf("remote media content type %q does not match %s", contentType, kind)
	}
	return c.storeAndSign(ctx, kind, contentType, &hardLimitReader{source: reader, remaining: maximum})
}

// LocalizeBase64 stores the media through the normal SSRF-hardened path, then
// encodes the cached bytes without downloading the upstream object a second time.
func (c *RemoteCache) LocalizeBase64(ctx context.Context, kind, rawURL string) (string, string, error) {
	signed, err := c.Localize(ctx, kind, rawURL)
	if err != nil {
		return "", "", err
	}
	parsed, err := url.Parse(signed)
	if err != nil {
		return "", "", fmt.Errorf("parse signed media URL: %w", err)
	}
	objectID, err := url.PathUnescape(path.Base(parsed.EscapedPath()))
	if err != nil || objectID == "." || objectID == "/" || objectID == "" {
		return "", "", errors.New("signed media URL contains an invalid object ID")
	}
	object, reader, err := c.Store.Open(ctx, objectID)
	if err != nil {
		return "", "", err
	}
	defer reader.Close()
	if object.Kind != kind {
		return "", "", fmt.Errorf("cached media kind %q does not match %s", object.Kind, kind)
	}
	var encoded strings.Builder
	if object.Size > 0 {
		encoded.Grow(base64.StdEncoding.EncodedLen(int(object.Size)))
	}
	encoder := base64.NewEncoder(base64.StdEncoding, &encoded)
	_, copyErr := io.Copy(encoder, reader)
	closeErr := encoder.Close()
	if copyErr != nil || closeErr != nil {
		return "", "", errors.Join(copyErr, closeErr)
	}
	return signed, encoded.String(), nil
}

func (c *RemoteCache) localizeDataURL(ctx context.Context, kind, rawURL string) (string, error) {
	trimmed := strings.TrimSpace(rawURL)
	if len(trimmed) < len("data:") || !strings.EqualFold(trimmed[:len("data:")], "data:") {
		return "", errors.New("invalid data URL")
	}
	header, encoded, ok := strings.Cut(trimmed[len("data:"):], ",")
	if !ok {
		return "", errors.New("invalid data URL")
	}
	parts := strings.Split(header, ";")
	contentType := strings.ToLower(strings.TrimSpace(parts[0]))
	if len(parts) < 2 || !strings.EqualFold(strings.TrimSpace(parts[len(parts)-1]), "base64") {
		return "", errors.New("media data URL must use base64 encoding")
	}
	if !strings.HasPrefix(contentType, kind+"/") {
		return "", fmt.Errorf("data URL content type %q does not match %s", contentType, kind)
	}
	maximum := c.MaxFetchBytes
	if maximum <= 0 {
		maximum = 25 << 20
	}
	if int64(base64.StdEncoding.DecodedLen(len(encoded))) > maximum+2 {
		return "", ErrMediaTooLarge
	}
	decoder := base64.NewDecoder(base64.StdEncoding, strings.NewReader(encoded))
	return c.storeAndSign(ctx, kind, contentType, &hardLimitReader{source: decoder, remaining: maximum})
}

func (c *RemoteCache) storeAndSign(ctx context.Context, kind, contentType string, source io.Reader) (string, error) {
	ttl := c.ImageTTL
	if kind == "video" {
		ttl = c.VideoTTL
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	object, err := c.Store.Put(ctx, kind, contentType, source, time.Now().UTC().Add(ttl))
	if err != nil {
		return "", err
	}
	signed, err := c.Signer.SignedURL(strings.TrimRight(c.PublicBaseURL, "/")+"/media", object.ID)
	if err != nil {
		_ = c.Store.Delete(context.Background(), object.ID)
		return "", err
	}
	return signed, nil
}

type hardLimitReader struct {
	source    io.Reader
	remaining int64
}

func (r *hardLimitReader) Read(buffer []byte) (int, error) {
	if r.remaining <= 0 {
		var probe [1]byte
		n, err := r.source.Read(probe[:])
		if n > 0 {
			return 0, ErrMediaTooLarge
		}
		return 0, err
	}
	if int64(len(buffer)) > r.remaining {
		buffer = buffer[:r.remaining]
	}
	n, err := r.source.Read(buffer)
	r.remaining -= int64(n)
	return n, err
}
