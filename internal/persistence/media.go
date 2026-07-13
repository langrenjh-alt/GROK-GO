package persistence

import (
	"context"
	"errors"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/domain"
	"github.com/langrenjh-alt/GROK-GO/internal/media"
	"github.com/langrenjh-alt/GROK-GO/internal/store"
)

type MediaAdmin struct {
	Store *media.FileStore
}

func (a MediaAdmin) ListMedia(ctx context.Context, pagination store.Pagination) ([]domain.MediaObject, int64, error) {
	if a.Store == nil {
		return nil, 0, errors.New("media store is not configured")
	}
	items, err := a.Store.List(ctx)
	if err != nil {
		return nil, 0, err
	}
	total := int64(len(items))
	limit, offset := pagination.Limit, pagination.Offset
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= len(items) {
		return []domain.MediaObject{}, total, nil
	}
	end := min(len(items), offset+limit)
	for index := offset; index < end; index++ {
		items[index].Path = ""
	}
	return items[offset:end], total, nil
}

func (a MediaAdmin) DeleteMedia(ctx context.Context, id string) error {
	if a.Store == nil {
		return errors.New("media store is not configured")
	}
	return a.Store.Delete(ctx, id)
}

func (a MediaAdmin) MediaSummary(ctx context.Context, expiringWithin time.Duration) (media.Summary, error) {
	if a.Store == nil {
		return media.Summary{}, errors.New("media store is not configured")
	}
	return a.Store.Summary(ctx, expiringWithin)
}

func (a MediaAdmin) DeleteMediaBatch(ctx context.Context, ids []string) (media.DeletionResult, error) {
	if a.Store == nil {
		return media.DeletionResult{}, errors.New("media store is not configured")
	}
	return a.Store.DeleteMany(ctx, ids)
}

func (a MediaAdmin) CleanupMedia(ctx context.Context, clearAll bool) (media.DeletionResult, error) {
	if a.Store == nil {
		return media.DeletionResult{}, errors.New("media store is not configured")
	}
	if clearAll {
		return a.Store.Clear(ctx)
	}
	return a.Store.CleanupExpired(ctx)
}
