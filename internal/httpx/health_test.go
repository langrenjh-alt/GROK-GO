package httpx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestReadiness(t *testing.T) {
	tests := []struct {
		name   string
		check  Check
		status int
	}{
		{
			name:   "ready",
			check:  Check{Name: "database", Run: func(context.Context) error { return nil }},
			status: http.StatusOK,
		},
		{
			name:   "dependency error",
			check:  Check{Name: "database", Run: func(context.Context) error { return errors.New("down") }},
			status: http.StatusServiceUnavailable,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			Readiness(time.Second, test.check).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d", recorder.Code, test.status)
			}
		})
	}
}
