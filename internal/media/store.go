package media

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/domain"
)

const (
	defaultCacheMaxBytes = int64(100 << 20)
	maxObjectBytes       = int64(1 << 30)
	maxMetadataBytes     = int64(64 << 10)
)

var (
	ErrMediaNotFound = errors.New("media object not found")
	ErrMediaTooLarge = errors.New("media object exceeds configured limit")
	ErrMediaCapacity = errors.New("media cache capacity cannot be reclaimed")
	fileStoreLocks   sync.Map
)

type Store interface {
	Put(context.Context, string, string, io.Reader, time.Time) (domain.MediaObject, error)
	Open(context.Context, string) (domain.MediaObject, io.ReadCloser, error)
	Delete(context.Context, string) error
}

type FileStore struct {
	root           string
	maxBytes       int64
	maxObjectBytes int64
	mu             *sync.RWMutex
	now            func() time.Time
}

func NewFileStore(root string, maxBytes int64) (*FileStore, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if maxBytes <= 0 {
		maxBytes = defaultCacheMaxBytes
	}
	if err := os.MkdirAll(absolute, 0o750); err != nil {
		return nil, err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, err
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return nil, err
	}
	sharedLock, _ := fileStoreLocks.LoadOrStore(canonical, &sync.RWMutex{})
	store := &FileStore{
		root:           canonical,
		maxBytes:       maxBytes,
		maxObjectBytes: min(maxBytes, maxObjectBytes),
		mu:             sharedLock.(*sync.RWMutex),
		now:            time.Now,
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.removeTemporaryFilesLocked()
	entries, usage, err := store.scanLocked(context.Background(), store.now().UTC())
	if err != nil {
		return nil, err
	}
	if usage > store.maxBytes {
		usage = store.evictLocked(context.Background(), entries, usage, store.lowWatermark(), "")
	}
	if usage > store.maxBytes {
		return nil, ErrMediaCapacity
	}
	return store, nil
}

func (s *FileStore) Put(ctx context.Context, kind, contentType string, source io.Reader, expiresAt time.Time) (domain.MediaObject, error) {
	if source == nil {
		return domain.MediaObject{}, errors.New("media source is required")
	}
	if err := ctx.Err(); err != nil {
		return domain.MediaObject{}, err
	}

	temporary, err := os.CreateTemp(s.root, ".upload-*")
	if err != nil {
		return domain.MediaObject{}, err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	_ = temporary.Chmod(0o640)
	written, copyErr := io.Copy(temporary, io.LimitReader(contextReader{ctx: ctx, reader: source}, s.maxObjectBytes+1))
	closeErr := temporary.Close()
	if copyErr != nil {
		return domain.MediaObject{}, copyErr
	}
	if closeErr != nil {
		return domain.MediaObject{}, closeErr
	}
	if written > s.maxObjectBytes {
		return domain.MediaObject{}, ErrMediaTooLarge
	}
	if err := ctx.Err(); err != nil {
		return domain.MediaObject{}, err
	}

	id := randomObjectID()
	path, err := s.objectPath(id, ".bin")
	if err != nil {
		return domain.MediaObject{}, err
	}
	metadataPath, err := s.objectPath(id, ".json")
	if err != nil {
		return domain.MediaObject{}, err
	}
	object := domain.MediaObject{
		ID:          id,
		Kind:        kind,
		Path:        path,
		ContentType: contentType,
		Size:        written,
		ExpiresAt:   expiresAt,
		CreatedAt:   s.now().UTC(),
	}
	metadata, err := json.Marshal(object)
	if err != nil {
		return domain.MediaObject{}, err
	}
	metadataTemporary, err := os.CreateTemp(s.root, ".metadata-*")
	if err != nil {
		return domain.MediaObject{}, err
	}
	metadataTemporaryName := metadataTemporary.Name()
	defer os.Remove(metadataTemporaryName)
	_ = metadataTemporary.Chmod(0o640)
	if _, err := metadataTemporary.Write(metadata); err != nil {
		_ = metadataTemporary.Close()
		return domain.MediaObject{}, err
	}
	if err := metadataTemporary.Close(); err != nil {
		return domain.MediaObject{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return domain.MediaObject{}, err
	}
	entries, usage, err := s.scanLocked(ctx, s.now().UTC())
	if err != nil {
		return domain.MediaObject{}, err
	}
	if exceedsCapacity(usage, written, s.maxBytes) {
		target := max(int64(0), s.lowWatermark()-written)
		usage = s.evictLocked(ctx, entries, usage, target, "")
		if exceedsCapacity(usage, written, s.maxBytes) {
			return domain.MediaObject{}, ErrMediaCapacity
		}
	}
	if err := ctx.Err(); err != nil {
		return domain.MediaObject{}, err
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return domain.MediaObject{}, err
	}
	if err := os.Rename(metadataTemporaryName, metadataPath); err != nil {
		_ = os.Remove(path)
		return domain.MediaObject{}, err
	}
	return object, nil
}

func (s *FileStore) Open(ctx context.Context, id string) (domain.MediaObject, io.ReadCloser, error) {
	if !validObjectID(id) {
		return domain.MediaObject{}, nil, ErrMediaNotFound
	}
	if err := ctx.Err(); err != nil {
		return domain.MediaObject{}, nil, err
	}
	metadataPath, err := s.objectPath(id, ".json")
	if err != nil {
		return domain.MediaObject{}, nil, ErrMediaNotFound
	}
	dataPath, err := s.objectPath(id, ".bin")
	if err != nil {
		return domain.MediaObject{}, nil, ErrMediaNotFound
	}
	s.mu.RLock()
	metadata, err := readRegularFile(metadataPath, maxMetadataBytes)
	if err != nil {
		s.mu.RUnlock()
		return domain.MediaObject{}, nil, ErrMediaNotFound
	}
	var object domain.MediaObject
	if json.Unmarshal(metadata, &object) != nil || object.ID != id || isExpired(object, s.now().UTC()) {
		s.mu.RUnlock()
		return domain.MediaObject{}, nil, ErrMediaNotFound
	}
	info, err := os.Lstat(dataPath)
	if err != nil || !info.Mode().IsRegular() {
		s.mu.RUnlock()
		return domain.MediaObject{}, nil, ErrMediaNotFound
	}
	file, err := os.Open(dataPath)
	if err != nil {
		s.mu.RUnlock()
		return domain.MediaObject{}, nil, ErrMediaNotFound
	}
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		_ = file.Close()
		s.mu.RUnlock()
		return domain.MediaObject{}, nil, ErrMediaNotFound
	}
	object.Path = dataPath
	object.Size = openedInfo.Size()
	return object, &lockedReadCloser{ReadCloser: file, unlock: s.mu.RUnlock}, nil
}

func (s *FileStore) Delete(ctx context.Context, id string) error {
	if !validObjectID(id) {
		return ErrMediaNotFound
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.removeObjectLocked(id)
}

func (s *FileStore) List(ctx context.Context) ([]domain.MediaObject, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, _, err := s.scanLocked(ctx, s.now().UTC())
	if err != nil {
		return nil, err
	}
	objects := make([]domain.MediaObject, 0, len(entries))
	for _, entry := range entries {
		objects = append(objects, entry.object)
	}
	slices.SortFunc(objects, func(left, right domain.MediaObject) int {
		return right.CreatedAt.Compare(left.CreatedAt)
	})
	return objects, nil
}

type cacheEntry struct {
	object domain.MediaObject
}

type dataFile struct {
	path string
	size int64
}

func (s *FileStore) scanLocked(ctx context.Context, now time.Time) ([]cacheEntry, int64, error) {
	directoryEntries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, 0, err
	}
	dataFiles := make(map[string]dataFile)
	var usage int64
	for _, entry := range directoryEntries {
		if err := ctx.Err(); err != nil {
			return nil, usage, err
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".bin" {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".bin")
		if !validObjectID(id) {
			continue
		}
		path, pathErr := s.objectPath(id, ".bin")
		if pathErr != nil {
			continue
		}
		info, statErr := os.Lstat(path)
		if statErr != nil {
			continue
		}
		if !info.Mode().IsRegular() {
			_ = os.Remove(path)
			continue
		}
		if info.Size() > 0 && usage > (1<<63-1)-info.Size() {
			return nil, usage, errors.New("media cache size overflow")
		}
		dataFiles[id] = dataFile{path: path, size: info.Size()}
		usage += info.Size()
	}

	objects := make([]cacheEntry, 0, len(dataFiles))
	validData := make(map[string]struct{}, len(dataFiles))
	for _, entry := range directoryEntries {
		if err := ctx.Err(); err != nil {
			return nil, usage, err
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
		data, hasData := dataFiles[id]
		var object domain.MediaObject
		if readErr != nil || json.Unmarshal(metadata, &object) != nil || object.ID != id || !hasData {
			dataGone, _ := s.removePathsLocked(metadataPath, data.path, hasData)
			if hasData && dataGone {
				usage -= data.size
				delete(dataFiles, id)
			}
			continue
		}
		if isExpired(object, now) {
			dataGone, _ := s.removePathsLocked(metadataPath, data.path, true)
			if dataGone {
				usage -= data.size
				delete(dataFiles, id)
			}
			continue
		}
		object.Path = data.path
		object.Size = data.size
		objects = append(objects, cacheEntry{object: object})
		validData[id] = struct{}{}
	}

	for id, data := range dataFiles {
		if _, ok := validData[id]; ok {
			continue
		}
		dataGone, _ := s.removePathsLocked("", data.path, true)
		if dataGone {
			usage -= data.size
		}
	}
	return objects, usage, nil
}

func (s *FileStore) evictLocked(ctx context.Context, entries []cacheEntry, usage, target int64, protectedID string) int64 {
	slices.SortFunc(entries, func(left, right cacheEntry) int {
		return left.object.CreatedAt.Compare(right.object.CreatedAt)
	})
	for _, entry := range entries {
		if usage <= target || ctx.Err() != nil {
			break
		}
		if entry.object.ID == protectedID {
			continue
		}
		if err := s.removeObjectLocked(entry.object.ID); err == nil {
			usage -= entry.object.Size
		}
	}
	return max(usage, 0)
}

func (s *FileStore) removeObjectLocked(id string) error {
	metadataPath, err := s.objectPath(id, ".json")
	if err != nil {
		return ErrMediaNotFound
	}
	dataPath, err := s.objectPath(id, ".bin")
	if err != nil {
		return ErrMediaNotFound
	}
	_, err = s.removePathsLocked(metadataPath, dataPath, true)
	return err
}

func (s *FileStore) removePathsLocked(metadataPath, dataPath string, hasData bool) (bool, error) {
	dataGone := !hasData
	if hasData {
		if err := os.Remove(dataPath); err != nil && !os.IsNotExist(err) {
			return false, err
		}
		dataGone = true
	}
	if metadataPath != "" {
		if err := os.Remove(metadataPath); err != nil && !os.IsNotExist(err) {
			return dataGone, err
		}
	}
	return dataGone, nil
}

func (s *FileStore) removeTemporaryFilesLocked() {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || (!strings.HasPrefix(entry.Name(), ".upload-") && !strings.HasPrefix(entry.Name(), ".metadata-")) {
			continue
		}
		_ = os.Remove(filepath.Join(s.root, entry.Name()))
	}
}

func (s *FileStore) objectPath(id, suffix string) (string, error) {
	if !validObjectID(id) || (suffix != ".bin" && suffix != ".json") {
		return "", ErrMediaNotFound
	}
	path := filepath.Join(s.root, id+suffix)
	relative, err := filepath.Rel(s.root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", ErrMediaNotFound
	}
	return path, nil
}

func (s *FileStore) lowWatermark() int64 {
	return (s.maxBytes/5)*3 + ((s.maxBytes%5)*3)/5
}

func exceedsCapacity(usage, addition, limit int64) bool {
	return addition > limit || usage > limit-addition
}

func isExpired(object domain.MediaObject, now time.Time) bool {
	return !object.ExpiresAt.IsZero() && !now.Before(object.ExpiresAt)
}

func readRegularFile(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, ErrMediaNotFound
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) || openedInfo.Size() > limit {
		return nil, ErrMediaNotFound
	}
	content, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(content)) > limit {
		return nil, ErrMediaNotFound
	}
	return content, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

type lockedReadCloser struct {
	io.ReadCloser
	unlock func()
	once   sync.Once
}

func (r *lockedReadCloser) Close() error {
	err := r.ReadCloser.Close()
	r.once.Do(r.unlock)
	return err
}

type DownloadHandler struct {
	Store  Store
	Signer *Signer
}

func (h DownloadHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if index := strings.LastIndexByte(id, '/'); index >= 0 {
		id = id[index+1:]
	}
	expires, err := strconv.ParseInt(r.URL.Query().Get("exp"), 10, 64)
	if err != nil || h.Signer == nil || h.Signer.Verify(id, expires, r.URL.Query().Get("sig")) != nil {
		http.Error(w, "invalid media signature", http.StatusForbidden)
		return
	}
	object, reader, err := h.Store.Open(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer reader.Close()
	w.Header().Set("Content-Type", object.ContentType)
	w.Header().Set("Content-Length", fmt.Sprint(object.Size))
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, reader)
}

func randomObjectID() string {
	var value [18]byte
	_, _ = rand.Read(value[:])
	return base64.RawURLEncoding.EncodeToString(value[:])
}
