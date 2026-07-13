package media

import (
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestRemoteCacheStoresAndSignsPublicMedia(t *testing.T) {
	store, err := NewFileStore(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := NewSigner([]byte(strings.Repeat("s", 32)), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"image/png"}}, Body: io.NopCloser(strings.NewReader("png-data")), Request: request}, nil
	})}
	cache := &RemoteCache{
		Store: store, Signer: signer, PublicBaseURL: "https://gateway.test",
		Client: client, Policy: SSRFPolicy{Resolver: staticResolver{"cdn.test": {{IP: net.ParseIP("1.1.1.1")}}}},
		MaxFetchBytes: 1024, ImageTTL: time.Hour,
	}
	signed, err := cache.Localize(context.Background(), "image", "https://cdn.test/image.png")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(signed)
	if err != nil {
		t.Fatal(err)
	}
	id := strings.TrimPrefix(parsed.Path, "/media/")
	if id == "" || parsed.Query().Get("sig") == "" || parsed.Query().Get("exp") == "" {
		t.Fatalf("unexpected signed URL: %s", signed)
	}
	object, reader, err := store.Open(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if object.Kind != "image" || object.ContentType != "image/png" || object.Size != 8 {
		t.Fatalf("unexpected cached object: %+v", object)
	}
}

func TestRemoteCacheLocalizeBase64UsesCachedBytes(t *testing.T) {
	store, err := NewFileStore(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := NewSigner([]byte(strings.Repeat("s", 32)), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	requests := 0
	cache := &RemoteCache{
		Store: store, Signer: signer, PublicBaseURL: "https://gateway.test",
		Client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			requests++
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"image/png"}}, Body: io.NopCloser(strings.NewReader("image-bytes")), Request: request}, nil
		})},
		Policy: SSRFPolicy{Resolver: staticResolver{"cdn.test": {{IP: net.ParseIP("1.1.1.1")}}}}, MaxFetchBytes: 1024, ImageTTL: time.Hour,
	}
	signed, encoded, err := cache.LocalizeBase64(context.Background(), "image", "https://cdn.test/image.png")
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 || encoded != base64.StdEncoding.EncodeToString([]byte("image-bytes")) || !strings.Contains(signed, "/media/") {
		t.Fatalf("localized base64 = requests:%d url:%q data:%q", requests, signed, encoded)
	}
}

func TestRemoteCacheRejectsOversizedAndPrivateMedia(t *testing.T) {
	store, _ := NewFileStore(t.TempDir(), 1<<20)
	signer, _ := NewSigner([]byte(strings.Repeat("s", 32)), time.Hour)
	cache := &RemoteCache{
		Store: store, Signer: signer, PublicBaseURL: "https://gateway.test", MaxFetchBytes: 4,
		Policy: SSRFPolicy{Resolver: staticResolver{
			"private.test": {{IP: net.ParseIP("127.0.0.1")}},
			"public.test":  {{IP: net.ParseIP("1.1.1.1")}},
		}},
		Client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, ContentLength: 5, Header: http.Header{"Content-Type": {"image/png"}}, Body: io.NopCloser(strings.NewReader("12345")), Request: request}, nil
		})},
	}
	if _, err := cache.Localize(context.Background(), "image", "https://private.test/a.png"); err == nil {
		t.Fatal("expected private media URL rejection")
	}
	if _, err := cache.Localize(context.Background(), "image", "https://public.test/a.png"); err == nil {
		t.Fatal("expected oversized media rejection")
	}
}

func TestRemoteCacheStoresBase64DataURL(t *testing.T) {
	store, err := NewFileStore(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := NewSigner([]byte(strings.Repeat("s", 32)), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cache := &RemoteCache{Store: store, Signer: signer, PublicBaseURL: "https://gateway.test", MaxFetchBytes: 1024, ImageTTL: time.Hour}
	raw := []byte("image-bytes")
	signed, err := cache.Localize(context.Background(), "image", "data:image/png;base64,"+base64.StdEncoding.EncodeToString(raw))
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(signed)
	id := strings.TrimPrefix(parsed.Path, "/media/")
	object, reader, err := store.Open(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	stored, _ := io.ReadAll(reader)
	if object.ContentType != "image/png" || string(stored) != string(raw) {
		t.Fatalf("stored data URL = %+v %q", object, stored)
	}
	if _, err := cache.Localize(context.Background(), "video", "data:image/png;base64,"+base64.StdEncoding.EncodeToString(raw)); err == nil {
		t.Fatal("mismatched data URL kind was accepted")
	}
}
