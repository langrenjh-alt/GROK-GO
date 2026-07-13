package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEmbeddedFilesystemIsAvailable(t *testing.T) {
	if _, err := Files(); err != nil {
		t.Fatalf("Files() error = %v", err)
	}
}

func TestStaticExportDirectoryRouteServesWithoutRedirect(t *testing.T) {
	handler, err := NewHandler()
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/login/", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "<!DOCTYPE html>") {
		t.Fatalf("status = %d, location = %q", response.Code, response.Header().Get("Location"))
	}
	if response.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("cache control = %q", response.Header().Get("Cache-Control"))
	}
}
