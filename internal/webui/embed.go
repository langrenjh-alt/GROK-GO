package webui

import (
	"bytes"
	"embed"
	"errors"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// dist is populated by scripts/build-web.ps1 before release builds.
//
//go:embed all:dist
var dist embed.FS

type Handler struct {
	files fs.FS
}

func NewHandler() (*Handler, error) {
	files, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil, err
	}
	return &Handler{files: files}, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.NotFound(w, r)
		return
	}

	requested := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if requested == "." || requested == "" {
		requested = "index.html"
	}
	if h.exists(requested) {
		h.serveFile(w, r, requested)
		return
	}
	if h.exists(path.Join(requested, "index.html")) {
		h.serveFile(w, r, path.Join(requested, "index.html"))
		return
	}
	if h.exists("index.html") {
		h.serveFile(w, r, "index.html")
		return
	}

	http.Error(w, "GROK-GO web assets are not embedded; run the web build first", http.StatusServiceUnavailable)
}

func (h *Handler) serveFile(w http.ResponseWriter, r *http.Request, name string) {
	content, err := fs.ReadFile(h.files, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if strings.HasPrefix(name, "_next/static/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeContent(w, r, path.Base(name), time.Time{}, bytes.NewReader(content))
}

func (h *Handler) exists(name string) bool {
	info, err := fs.Stat(h.files, name)
	return err == nil && !info.IsDir()
}

func Files() (fs.FS, error) {
	files, err := fs.Sub(dist, "dist")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fs.ErrNotExist
	}
	return files, err
}
