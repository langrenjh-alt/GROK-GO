package requestlog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/apikey"
	"github.com/langrenjh-alt/GROK-GO/internal/domain"
	"github.com/langrenjh-alt/GROK-GO/internal/httpx"
	"github.com/langrenjh-alt/GROK-GO/internal/security"
)

type Sink interface {
	CreateRequestLog(context.Context, *domain.RequestLog) error
}

type Middleware struct {
	Sink Sink
	Now  func() time.Time
}

func (m Middleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		if m.Now != nil {
			started = m.Now()
		}
		model := inspectModel(r)
		wrapped := &responseWriter{ResponseWriter: w, status: http.StatusOK}
		requestContext, outcomeTracker := httpx.WithOutcome(r.Context())
		next.ServeHTTP(wrapped, r.WithContext(requestContext))
		if m.Sink == nil {
			return
		}
		id, err := security.GenerateID()
		if err != nil {
			return
		}
		key, _ := apikey.FromContext(r.Context())
		completion, _ := apikey.CompletionFromContext(r.Context())
		statusCode := wrapped.status
		errorCode := ""
		if outcome := outcomeTracker.Snapshot(); outcome.StatusCode > 0 {
			statusCode = outcome.StatusCode
			errorCode = outcome.ErrorCode
		}
		entry := &domain.RequestLog{
			ID:           id,
			RequestID:    httpx.RequestIDFromContext(r.Context()),
			ClientKeyID:  key.ID,
			AccountID:    completion.AccountID,
			Model:        model,
			Endpoint:     r.URL.Path,
			StatusCode:   statusCode,
			DurationMS:   time.Since(started).Milliseconds(),
			InputTokens:  completion.InputTokens,
			OutputTokens: completion.OutputTokens,
			CachedTokens: completion.CachedTokens,
			UsageParsed:  completion.UsageParsed,
			CreatedAt:    started.UTC(),
		}
		if statusCode >= http.StatusBadRequest {
			entry.ErrorCode = errorCode
			if entry.ErrorCode == "" {
				entry.ErrorCode = fmt.Sprintf("http_%d", statusCode)
			}
			entry.ErrorSummary = http.StatusText(statusCode)
			if statusCode == 499 {
				entry.ErrorSummary = "Client Closed Request"
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = m.Sink.CreateRequestLog(ctx, entry)
	})
}

func inspectModel(r *http.Request) string {
	if r.Body == nil || !strings.Contains(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		return ""
	}
	const maximum = 1 << 20
	body, err := io.ReadAll(io.LimitReader(r.Body, maximum+1))
	if err != nil {
		return ""
	}
	r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), r.Body))
	if len(body) > maximum {
		return ""
	}
	var value struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &value)
	return value.Model
}

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (w *responseWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *responseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
