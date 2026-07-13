package media

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/domain"
)

const DefaultExpiringSoonWindow = 24 * time.Hour

type Summary struct {
	TotalObjects        int       `json:"total_objects"`
	TotalBytes          int64     `json:"total_bytes"`
	ImageObjects        int       `json:"image_objects"`
	ImageBytes          int64     `json:"image_bytes"`
	VideoObjects        int       `json:"video_objects"`
	VideoBytes          int64     `json:"video_bytes"`
	ExpiringSoonObjects int       `json:"expiring_soon_objects"`
	ExpiringSoonBytes   int64     `json:"expiring_soon_bytes"`
	ExpiringBefore      time.Time `json:"expiring_before"`
}

type DeletionError struct {
	ID      string `json:"id"`
	Message string `json:"message"`
}

type DeletionResult struct {
	Requested    int             `json:"requested"`
	Deleted      int             `json:"deleted"`
	DeletedBytes int64           `json:"deleted_bytes"`
	Failed       int             `json:"failed"`
	Errors       []DeletionError `json:"errors"`
}

func (s *FileStore) Summary(ctx context.Context, expiringWithin time.Duration) (Summary, error) {
	if err := ctx.Err(); err != nil {
		return Summary{}, err
	}
	if expiringWithin <= 0 {
		expiringWithin = DefaultExpiringSoonWindow
	}
	now := s.now().UTC()
	result := Summary{ExpiringBefore: now.Add(expiringWithin)}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, usage, err := s.scanLocked(ctx, now)
	if err != nil {
		return Summary{}, err
	}
	result.TotalObjects = len(entries)
	result.TotalBytes = usage
	for _, entry := range entries {
		object := entry.object
		switch object.Kind {
		case "image":
			result.ImageObjects++
			result.ImageBytes += object.Size
		case "video":
			result.VideoObjects++
			result.VideoBytes += object.Size
		}
		if !object.ExpiresAt.IsZero() && object.ExpiresAt.After(now) && !object.ExpiresAt.After(result.ExpiringBefore) {
			result.ExpiringSoonObjects++
			result.ExpiringSoonBytes += object.Size
		}
	}
	return result, nil
}

func (s *FileStore) DeleteMany(ctx context.Context, ids []string) (DeletionResult, error) {
	if err := ctx.Err(); err != nil {
		return DeletionResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deleteIDsLocked(ctx, uniqueObjectIDs(ids))
}

func (s *FileStore) CleanupExpired(ctx context.Context) (DeletionResult, error) {
	if err := ctx.Err(); err != nil {
		return DeletionResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	directoryEntries, err := os.ReadDir(s.root)
	if err != nil {
		return DeletionResult{}, err
	}
	now := s.now().UTC()
	ids := make([]string, 0)
	for _, entry := range directoryEntries {
		if err := ctx.Err(); err != nil {
			return DeletionResult{}, err
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !validObjectID(id) {
			continue
		}
		metadataPath, pathErr := s.objectPath(id, ".json")
		if pathErr != nil {
			continue
		}
		metadata, readErr := readRegularFile(metadataPath, maxMetadataBytes)
		var object domain.MediaObject
		if readErr == nil && json.Unmarshal(metadata, &object) == nil && object.ID == id && isExpired(object, now) {
			ids = append(ids, id)
		}
	}
	result, err := s.deleteIDsLocked(ctx, uniqueObjectIDs(ids))
	if err != nil {
		return result, err
	}
	// Reuse the normal scanner to remove invalid metadata and orphan payloads.
	_, _, err = s.scanLocked(ctx, now)
	return result, err
}

func (s *FileStore) Clear(ctx context.Context) (DeletionResult, error) {
	if err := ctx.Err(); err != nil {
		return DeletionResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	directoryEntries, err := os.ReadDir(s.root)
	if err != nil {
		return DeletionResult{}, err
	}
	ids := make([]string, 0, len(directoryEntries))
	for _, entry := range directoryEntries {
		if err := ctx.Err(); err != nil {
			return DeletionResult{}, err
		}
		if entry.IsDir() {
			continue
		}
		extension := filepath.Ext(entry.Name())
		if extension != ".bin" && extension != ".json" {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), extension)
		if validObjectID(id) {
			ids = append(ids, id)
		}
	}
	result, err := s.deleteIDsLocked(ctx, uniqueObjectIDs(ids))
	if err == nil {
		s.removeTemporaryFilesLocked()
	}
	return result, err
}

func (s *FileStore) deleteIDsLocked(ctx context.Context, ids []string) (DeletionResult, error) {
	result := DeletionResult{Requested: len(ids), Errors: make([]DeletionError, 0)}
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		size, exists, err := s.objectFootprintLocked(id)
		if err != nil {
			result.Errors = append(result.Errors, DeletionError{ID: id, Message: err.Error()})
			continue
		}
		if !exists {
			result.Errors = append(result.Errors, DeletionError{ID: id, Message: ErrMediaNotFound.Error()})
			continue
		}
		if err := s.removeObjectLocked(id); err != nil {
			result.Errors = append(result.Errors, DeletionError{ID: id, Message: err.Error()})
			continue
		}
		result.Deleted++
		result.DeletedBytes += size
	}
	result.Failed = len(result.Errors)
	return result, nil
}

func (s *FileStore) objectFootprintLocked(id string) (int64, bool, error) {
	if !validObjectID(id) {
		return 0, false, ErrMediaNotFound
	}
	dataPath, err := s.objectPath(id, ".bin")
	if err != nil {
		return 0, false, ErrMediaNotFound
	}
	metadataPath, err := s.objectPath(id, ".json")
	if err != nil {
		return 0, false, ErrMediaNotFound
	}
	exists := false
	var size int64
	if info, statErr := os.Lstat(dataPath); statErr == nil {
		exists = true
		if info.Mode().IsRegular() {
			size = info.Size()
		}
	} else if !os.IsNotExist(statErr) {
		return 0, false, statErr
	}
	if _, statErr := os.Lstat(metadataPath); statErr == nil {
		exists = true
	} else if !os.IsNotExist(statErr) {
		return 0, false, statErr
	}
	return size, exists, nil
}

func uniqueObjectIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	slices.Sort(result)
	return result
}
