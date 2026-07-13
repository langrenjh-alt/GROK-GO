package persistence

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/media"
	"github.com/langrenjh-alt/GROK-GO/internal/store"
)

func TestListMediaReturnsPageAndFullTotal(t *testing.T) {
	fileStore, err := media.NewFileStore(t.TempDir(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 3; index++ {
		if _, err := fileStore.Put(context.Background(), "image", "image/png", strings.NewReader("image"), time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}

	items, total, err := (MediaAdmin{Store: fileStore}).ListMedia(context.Background(), store.Pagination{Limit: 1, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || total != 3 {
		t.Fatalf("media page length = %d, total = %d", len(items), total)
	}
	if items[0].Path != "" {
		t.Fatal("media page exposed its storage path")
	}
}
