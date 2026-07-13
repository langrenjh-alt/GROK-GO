package media

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMediaSummaryCountsKindsBytesAndExpiringObjects(t *testing.T) {
	store, err := NewFileStore(t.TempDir(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	if _, err := store.Put(context.Background(), "image", "image/png", strings.NewReader(strings.Repeat("i", 10)), now.Add(12*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(context.Background(), "video", "video/mp4", strings.NewReader(strings.Repeat("v", 20)), now.Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}

	summary, err := store.Summary(context.Background(), 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if summary.TotalObjects != 2 || summary.TotalBytes != 30 || summary.ImageObjects != 1 || summary.ImageBytes != 10 || summary.VideoObjects != 1 || summary.VideoBytes != 20 {
		t.Fatalf("summary = %+v", summary)
	}
	if summary.ExpiringSoonObjects != 1 || summary.ExpiringSoonBytes != 10 || !summary.ExpiringBefore.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("expiring summary = %+v", summary)
	}
}

func TestDeleteManyRejectsPathsAndReportsActualDeletion(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root, 1024)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	first, err := store.Put(context.Background(), "image", "image/png", strings.NewReader("first"), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Put(context.Background(), "video", "video/mp4", strings.NewReader("second"), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.bin")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := store.DeleteMany(context.Background(), []string{first.ID, first.ID, "../../outside", "missing_object"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Requested != 3 || result.Deleted != 1 || result.DeletedBytes != int64(len("first")) || result.Failed != 2 || len(result.Errors) != 2 {
		t.Fatalf("deletion result = %+v", result)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside file was affected: %v", err)
	}
	if _, reader, err := store.Open(context.Background(), first.ID); !errors.Is(err, ErrMediaNotFound) {
		if reader != nil {
			_ = reader.Close()
		}
		t.Fatalf("deleted object remained available: %v", err)
	}
	if _, reader, err := store.Open(context.Background(), second.ID); err != nil {
		t.Fatalf("unselected object was deleted: %v", err)
	} else {
		_ = reader.Close()
	}
}

func TestCleanupExpiredAndClearReturnDeletedFootprint(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root, 1024)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	expiring, err := store.Put(context.Background(), "image", "image/png", strings.NewReader(strings.Repeat("x", 11)), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	remaining, err := store.Put(context.Background(), "video", "video/mp4", strings.NewReader(strings.Repeat("y", 19)), now.Add(4*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)

	cleaned, err := store.CleanupExpired(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cleaned.Requested != 1 || cleaned.Deleted != 1 || cleaned.DeletedBytes != 11 || cleaned.Failed != 0 {
		t.Fatalf("expired cleanup = %+v", cleaned)
	}
	if _, reader, err := store.Open(context.Background(), expiring.ID); !errors.Is(err, ErrMediaNotFound) {
		if reader != nil {
			_ = reader.Close()
		}
		t.Fatalf("expired object remained available: %v", err)
	}

	invalidCacheFile := filepath.Join(root, "..invalid.bin")
	if err := os.WriteFile(invalidCacheFile, []byte("leave-me"), 0o600); err != nil {
		t.Fatal(err)
	}
	cleared, err := store.Clear(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Requested != 1 || cleared.Deleted != 1 || cleared.DeletedBytes != 19 || cleared.Failed != 0 {
		t.Fatalf("clear result = %+v", cleared)
	}
	if _, reader, err := store.Open(context.Background(), remaining.ID); !errors.Is(err, ErrMediaNotFound) {
		if reader != nil {
			_ = reader.Close()
		}
		t.Fatalf("cleared object remained available: %v", err)
	}
	if content, err := os.ReadFile(invalidCacheFile); err != nil || string(content) != "leave-me" {
		t.Fatalf("clear crossed object ID boundary: %q, %v", content, err)
	}
}
