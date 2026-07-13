package media

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

type staticResolver map[string][]net.IPAddr

func (r staticResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	return r[host], nil
}

func TestSignerRejectsTamperingAndExpiry(t *testing.T) {
	signer, err := NewSigner([]byte(strings.Repeat("k", 32)), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	signer.now = func() time.Time { return now }
	expires := now.Add(time.Minute).Unix()
	signature := signer.Sign("media_123456", expires)
	if err := signer.Verify("media_123456", expires, signature); err != nil {
		t.Fatal(err)
	}
	if err := signer.Verify("media_123457", expires, signature); err == nil {
		t.Fatal("expected tamper rejection")
	}
	signer.now = func() time.Time { return now.Add(2 * time.Minute) }
	if err := signer.Verify("media_123456", expires, signature); err == nil {
		t.Fatal("expected expiry rejection")
	}
}

func TestValidateURLBlocksPrivateAndAllowsPublic(t *testing.T) {
	resolver := staticResolver{
		"private.test": {{IP: net.ParseIP("127.0.0.1")}},
		"public.test":  {{IP: net.ParseIP("1.1.1.1")}},
	}
	if _, err := ValidateURL(context.Background(), "https://private.test/file", SSRFPolicy{Resolver: resolver}); err == nil {
		t.Fatal("expected private address rejection")
	}
	if _, err := ValidateURL(context.Background(), "https://public.test/file", SSRFPolicy{Resolver: resolver}); err != nil {
		t.Fatal(err)
	}
}

func TestFileStoreRoundTrip(t *testing.T) {
	store, err := NewFileStore(t.TempDir(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	object, err := store.Put(context.Background(), "image", "image/png", strings.NewReader("image"), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	loaded, reader, err := store.Open(context.Background(), object.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if loaded.Size != 5 || loaded.ContentType != "image/png" {
		t.Fatalf("unexpected object: %#v", loaded)
	}
}
