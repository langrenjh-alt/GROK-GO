package media

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/domain"
)

func TestFileStoreEvictsOldestObjectsToLowWatermark(t *testing.T) {
	store, err := NewFileStore(t.TempDir(), 100)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	store.now = func() time.Time { return now }

	objects := make([]domain.MediaObject, 0, 4)
	for range 4 {
		object, putErr := store.Put(
			context.Background(),
			"image",
			"image/png",
			strings.NewReader(strings.Repeat("x", 30)),
			now.Add(time.Hour),
		)
		if putErr != nil {
			t.Fatal(putErr)
		}
		objects = append(objects, object)
		now = now.Add(time.Second)
	}

	if usage := cachedPayloadBytes(t, store.root); usage != 60 {
		t.Fatalf("cache usage = %d, want 60-byte low watermark", usage)
	}
	for _, object := range objects[:2] {
		if _, reader, openErr := store.Open(context.Background(), object.ID); !errors.Is(openErr, ErrMediaNotFound) {
			if reader != nil {
				_ = reader.Close()
			}
			t.Fatalf("old object %s was not evicted: %v", object.ID, openErr)
		}
	}
	for _, object := range objects[2:] {
		_, reader, openErr := store.Open(context.Background(), object.ID)
		if openErr != nil {
			t.Fatalf("recent object %s was evicted: %v", object.ID, openErr)
		}
		_ = reader.Close()
	}
}

func TestFileStoreCleansObjectsThatExpireDuringPut(t *testing.T) {
	store, err := NewFileStore(t.TempDir(), 100)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	store.now = func() time.Time { return now }
	old, err := store.Put(
		context.Background(),
		"image",
		"image/png",
		strings.NewReader(strings.Repeat("o", 70)),
		now.Add(time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}

	reader := &advancingReader{
		Reader: strings.NewReader(strings.Repeat("n", 50)),
		advance: func() {
			now = now.Add(2 * time.Minute)
		},
	}
	recent, err := store.Put(
		context.Background(),
		"image",
		"image/png",
		reader,
		now.Add(time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, opened, openErr := store.Open(context.Background(), old.ID); !errors.Is(openErr, ErrMediaNotFound) {
		if opened != nil {
			_ = opened.Close()
		}
		t.Fatalf("expired object remained available: %v", openErr)
	}
	_, opened, err := store.Open(context.Background(), recent.ID)
	if err != nil {
		t.Fatal(err)
	}
	_ = opened.Close()
	if usage := cachedPayloadBytes(t, store.root); usage != 50 {
		t.Fatalf("cache usage = %d, want only the 50-byte recent object", usage)
	}
}

func TestFileStoreConcurrentPutsStayWithinCapacity(t *testing.T) {
	store, err := NewFileStore(t.TempDir(), 100)
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	errorsCh := make(chan error, 32)
	for range 32 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, putErr := store.Put(
				context.Background(),
				"image",
				"image/png",
				strings.NewReader(strings.Repeat("x", 10)),
				time.Now().Add(time.Hour),
			)
			if putErr != nil {
				errorsCh <- putErr
			}
		}()
	}
	wait.Wait()
	close(errorsCh)
	for putErr := range errorsCh {
		t.Errorf("concurrent Put failed: %v", putErr)
	}
	if usage := cachedPayloadBytes(t, store.root); usage > 100 {
		t.Fatalf("cache usage = %d, exceeds 100-byte capacity", usage)
	}
}

func TestFileStoreSlowSourceDoesNotBlockAnotherPut(t *testing.T) {
	store, err := NewFileStore(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()

	slowDone := make(chan error, 1)
	go func() {
		_, putErr := store.Put(context.Background(), "image", "image/png", &blockingReader{
			Reader: strings.NewReader(strings.Repeat("s", 32)), started: started, release: release,
		}, time.Now().Add(time.Hour))
		slowDone <- putErr
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("slow source did not start")
	}

	fastDone := make(chan error, 1)
	go func() {
		_, putErr := store.Put(context.Background(), "image", "image/png", strings.NewReader("fast"), time.Now().Add(time.Hour))
		fastDone <- putErr
	}()
	select {
	case putErr := <-fastDone:
		if putErr != nil {
			t.Fatalf("fast Put failed: %v", putErr)
		}
	case <-time.After(2 * time.Second):
		unblock()
		<-slowDone
		<-fastDone
		t.Fatal("fast Put was blocked by an unrelated slow source")
	}

	unblock()
	if putErr := <-slowDone; putErr != nil {
		t.Fatalf("slow Put failed: %v", putErr)
	}
}

func TestFileStoreRetainsSeparateSingleObjectLimit(t *testing.T) {
	store, err := NewFileStore(t.TempDir(), 32)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Put(
		context.Background(),
		"image",
		"image/png",
		strings.NewReader(strings.Repeat("x", 33)),
		time.Now().Add(time.Hour),
	)
	if !errors.Is(err, ErrMediaTooLarge) {
		t.Fatalf("Put error = %v, want ErrMediaTooLarge", err)
	}
	if usage := cachedPayloadBytes(t, store.root); usage != 0 {
		t.Fatalf("oversized object left %d payload bytes behind", usage)
	}
}

func TestFileStoreDoesNotTrustMetadataPathsOrSymlinks(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root, 1024)
	if err != nil {
		t.Fatal(err)
	}
	outsidePath := filepath.Join(t.TempDir(), "outside.bin")
	if err := os.WriteFile(outsidePath, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}

	id := "metadata_outside"
	metadata := map[string]any{
		"id":           id,
		"kind":         "image",
		"content_type": "image/png",
		"size":         7,
		"path":         outsidePath,
		"created_at":   time.Now().UTC(),
		"expires_at":   time.Now().Add(time.Hour).UTC(),
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, id+".json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, reader, openErr := store.Open(context.Background(), id); !errors.Is(openErr, ErrMediaNotFound) {
		if reader != nil {
			_ = reader.Close()
		}
		t.Fatalf("metadata path escaped the cache root: %v", openErr)
	}
	if _, err := os.Stat(outsidePath); err != nil {
		t.Fatalf("outside file was touched: %v", err)
	}
	if _, reader, openErr := store.Open(context.Background(), "../outside"); !errors.Is(openErr, ErrMediaNotFound) {
		if reader != nil {
			_ = reader.Close()
		}
		t.Fatalf("traversal id error = %v, want ErrMediaNotFound", openErr)
	}

	symlinkID := "symlink_object"
	if err := os.Symlink(outsidePath, filepath.Join(root, symlinkID+".bin")); err != nil {
		t.Logf("symlink check skipped: %v", err)
		return
	}
	symlinkObject := domain.MediaObject{
		ID:          symlinkID,
		Kind:        "image",
		ContentType: "image/png",
		Size:        7,
		CreatedAt:   time.Now().UTC(),
		ExpiresAt:   time.Now().Add(time.Hour).UTC(),
	}
	encoded, err = json.Marshal(symlinkObject)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, symlinkID+".json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, reader, openErr := store.Open(context.Background(), symlinkID); !errors.Is(openErr, ErrMediaNotFound) {
		if reader != nil {
			_ = reader.Close()
		}
		t.Fatalf("symlink object was opened: %v", openErr)
	}
	if _, err := store.List(context.Background()); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(outsidePath)
	if err != nil || string(content) != "outside" {
		t.Fatalf("outside symlink target changed: content=%q err=%v", content, err)
	}
}

type advancingReader struct {
	io.Reader
	once    sync.Once
	advance func()
}

type blockingReader struct {
	io.Reader
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *blockingReader) Read(buffer []byte) (int, error) {
	r.once.Do(func() { close(r.started) })
	<-r.release
	return r.Reader.Read(buffer)
}

func (r *advancingReader) Read(buffer []byte) (int, error) {
	r.once.Do(r.advance)
	return r.Reader.Read(buffer)
}

func cachedPayloadBytes(t *testing.T, root string) int64 {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".bin" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		total += info.Size()
	}
	return total
}
