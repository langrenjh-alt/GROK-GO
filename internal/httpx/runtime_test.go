package httpx

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestDynamicCORSReadsUpdatedOrigins(t *testing.T) {
	configured := "https://first.test"
	handler := DynamicCORS(func() string { return configured })(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header.Set("Origin", "https://second.test")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("origin allowed before update: %q", got)
	}

	configured = "https://second.test"
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "https://second.test" {
		t.Fatalf("allowed origin = %q", got)
	}
}

func TestDynamicCORSRejectsDisallowedPreflight(t *testing.T) {
	handler := DynamicCORS(func() string { return "https://allowed.test" })(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("preflight reached application handler")
	}))
	request := httptest.NewRequest(http.MethodOptions, "/v1/responses", nil)
	request.Header.Set("Origin", "https://denied.test")
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d", recorder.Code)
	}
}

func TestDynamicConcurrencyRejectsExcessRequest(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	handler := DynamicConcurrency(func() int { return 1 })(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		w.WriteHeader(http.StatusOK)
	}))

	var wait sync.WaitGroup
	wait.Add(1)
	go func() {
		defer wait.Done()
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
	}()
	<-entered
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
	close(release)
	wait.Wait()
	if recorder.Code != http.StatusTooManyRequests || recorder.Header().Get("Retry-After") != "1" {
		t.Fatalf("status = %d, retry-after = %q", recorder.Code, recorder.Header().Get("Retry-After"))
	}
}
