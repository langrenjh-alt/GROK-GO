package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/accounts"
	"github.com/langrenjh-alt/GROK-GO/internal/domain"
	"github.com/langrenjh-alt/GROK-GO/internal/upstream"
)

type Config struct {
	Models            ModelSource
	Accounts          *accounts.Pool
	Upstream          upstream.Client
	Videos            VideoStore
	Media             MediaLocalizer
	ProxyURL          func(context.Context, string) (string, error)
	RefreshAccount    func(context.Context, string) error
	OnCompletion      func(context.Context, Completion)
	HeartbeatInterval time.Duration
	MaxBodyBytes      int64
	MaxBodyBytesFunc  func() int64
	MaxAttempts       int
	BackgroundContext context.Context
	VideoPollInterval time.Duration
	VideoPollTimeout  time.Duration
}

// Completion is emitted once the selected upstream response has finished. A
// usage value is billable only when UsageParsed is true.
type Completion struct {
	AccountID                string
	Usage                    upstream.Usage
	UsageParsed              bool
	CacheIdentityApplied     bool
	CacheAffinityReused      bool
	CacheAffinityEstablished bool
}

type Handler struct {
	config  Config
	initErr error
}

// New returns an http.Handler so the gateway can be mounted directly by cmd or
// a larger admin router. Invalid configuration is surfaced as a JSON 500.
func New(config Config) http.Handler {
	handler, err := NewHandler(config)
	if err != nil {
		return &Handler{config: config, initErr: err}
	}
	return handler
}

func NewHandler(config Config) (*Handler, error) {
	if config.Models == nil || config.Accounts == nil || config.Upstream == nil {
		return nil, errors.New("gateway requires model source, account pool, and upstream client")
	}
	if config.HeartbeatInterval <= 0 {
		config.HeartbeatInterval = 15 * time.Second
	}
	if config.MaxBodyBytes <= 0 {
		config.MaxBodyBytes = 16 << 20
	}
	if config.MaxAttempts <= 0 {
		config.MaxAttempts = 3
	}
	if config.VideoPollInterval <= 0 {
		config.VideoPollInterval = 2 * time.Second
	}
	if config.VideoPollTimeout <= 0 {
		config.VideoPollTimeout = 10 * time.Minute
	}
	return &Handler{config: config}, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/")
	pathOperation := operationForPath(path)
	if h.initErr != nil {
		writeOperationError(w, pathOperation, http.StatusInternalServerError, h.initErr.Error(), "server_error", "gateway_not_configured", "")
		return
	}
	if r.Method == http.MethodGet && path == "/v1/models" {
		h.models(w, r)
		return
	}
	if r.Method == http.MethodGet && strings.HasPrefix(path, "/v1/models/") {
		h.model(w, r, strings.TrimPrefix(path, "/v1/models/"))
		return
	}
	if r.Method == http.MethodGet && strings.HasPrefix(path, "/v1/videos/") {
		h.videoGet(w, r, strings.TrimPrefix(path, "/v1/videos/"))
		return
	}
	if r.Method != http.MethodPost {
		writeOperationError(w, pathOperation, http.StatusNotFound, "Route not found", "invalid_request_error", "not_found", "")
		return
	}
	var operation upstream.Operation
	switch path {
	case "/v1/chat/completions":
		operation = upstream.OperationChat
	case "/v1/responses":
		operation = upstream.OperationResponses
	case "/v1/messages":
		operation = upstream.OperationMessages
	case "/v1/images/generations":
		operation = upstream.OperationImage
	case "/v1/images/edits":
		operation = upstream.OperationImageEdit
	case "/v1/videos", "/v1/videos/generations":
		operation = upstream.OperationVideo
	default:
		writeError(w, http.StatusNotFound, "Route not found", "invalid_request_error", "not_found", "")
		return
	}
	h.handleOperation(w, r, operation)
}

func (h *Handler) models(w http.ResponseWriter, r *http.Request) {
	models, err := h.config.Models.ListModels(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to load models", "server_error", "model_list_failed", "")
		return
	}
	data := make([]map[string]any, 0, len(models))
	for _, model := range models {
		data = append(data, map[string]any{"id": model.ID, "object": "model", "created": model.CreatedAt.Unix(), "owned_by": "xai", "name": model.DisplayName})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

func (h *Handler) model(w http.ResponseWriter, r *http.Request, modelID string) {
	model, err := h.config.Models.ResolveModel(r.Context(), modelID)
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("Model %q does not exist", modelID), "invalid_request_error", "model_not_found", "model")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": model.ID, "object": "model", "created": model.CreatedAt.Unix(), "owned_by": "xai", "name": model.DisplayName})
}

func (h *Handler) handleOperation(w http.ResponseWriter, r *http.Request, operation upstream.Operation) {
	body, payload, err := h.readPayload(w, r)
	if err != nil {
		writeOperationError(w, operation, http.StatusBadRequest, err.Error(), "invalid_request_error", "invalid_json", "body")
		return
	}
	modelName, _ := payload["model"].(string)
	if strings.TrimSpace(modelName) == "" {
		writeOperationError(w, operation, http.StatusBadRequest, "model is required", "invalid_request_error", "missing_parameter", "model")
		return
	}
	model, err := h.config.Models.ResolveModel(r.Context(), modelName)
	if err != nil || !allowsOperation(model, operation) {
		writeOperationError(w, operation, http.StatusNotFound, fmt.Sprintf("Model %q does not exist or is unavailable for this endpoint", modelName), "invalid_request_error", "model_not_found", "model")
		return
	}
	if issue := validateOperationPayload(operation, payload); issue != nil {
		writeOperationError(w, operation, http.StatusBadRequest, issue.message, "invalid_request_error", issue.code, issue.param)
		return
	}
	stream, _ := payload["stream"].(bool)
	cacheIdentity := requestCacheIdentities(r, model.ID, payload)
	response, lease, err := h.invoke(r.Context(), operation, model, body, r.Header, stream, cacheIdentity.SessionAffinityKey, cacheIdentity.PromptCacheKey)
	if err != nil {
		status := http.StatusBadGateway
		code := "upstream_error"
		if errors.Is(err, accounts.ErrNoAccount) {
			status, code = http.StatusTooManyRequests, "rate_limit_exceeded"
		}
		writeOperationError(w, operation, status, err.Error(), errorType(status), code, "")
		return
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message := upstreamErrorMessage(response)
		h.reportCompletion(r.Context(), lease, upstream.Usage{}, false)
		_ = lease.Release(context.Background(), feedbackFromResponse(response, errors.New(message)))
		writeOperationError(w, operation, mapStatus(response.StatusCode), message, errorType(mapStatus(response.StatusCode)), "upstream_error", "")
		return
	}
	if stream && isConversational(operation) {
		h.stream(w, r, operation, model.ID, response, lease)
		return
	}
	h.nonStream(w, r, operation, model.ID, payload, response, lease)
}

func (h *Handler) reportCompletion(ctx context.Context, lease *accounts.Lease, value upstream.Usage, parsed bool) {
	if h.config.OnCompletion == nil {
		return
	}
	completion := Completion{Usage: value, UsageParsed: parsed}
	if lease != nil {
		completion.AccountID = lease.Account.ID
		completion.CacheIdentityApplied = lease.CacheIdentityApplied
		completion.CacheAffinityReused = lease.AffinityReused
		completion.CacheAffinityEstablished = lease.CacheAffinityEstablished
	}
	h.config.OnCompletion(ctx, completion)
}

func (h *Handler) invoke(ctx context.Context, operation upstream.Operation, model domain.ModelSpec, body json.RawMessage, headers http.Header, stream bool, affinity, promptCacheKey string) (*upstream.Response, *accounts.Lease, error) {
	excluded := make(map[string]struct{})
	refreshed := make(map[string]struct{})
	var lastErr error
	for attempt := 0; attempt < h.config.MaxAttempts; attempt++ {
		lease, err := h.config.Accounts.Acquire(ctx, accounts.Selection{Model: model, AffinityKey: affinity, ExcludeIDs: excluded})
		if err != nil {
			if lastErr != nil {
				return nil, nil, lastErr
			}
			return nil, nil, err
		}
		proxyURL := ""
		if lease.Account.ProxyID != "" && h.config.ProxyURL != nil {
			proxyURL, err = h.config.ProxyURL(ctx, lease.Account.ProxyID)
			if err != nil {
				lastErr = err
				excluded[lease.Account.ID] = struct{}{}
				_ = lease.Release(context.Background(), accounts.Feedback{StatusCode: http.StatusBadGateway, Err: err})
				continue
			}
		}
		response, err := h.config.Upstream.Do(ctx, upstream.Request{Operation: operation, Model: model.ID, UpstreamModel: model.UpstreamModel, PromptCacheKey: promptCacheKey, CredentialKind: lease.Account.Kind, Credentials: lease.Credentials, ProxyURL: proxyURL, Body: body, Headers: forwardedHeaders(headers), Stream: stream})
		if err != nil {
			lastErr = err
			excluded[lease.Account.ID] = struct{}{}
			_ = lease.Release(context.Background(), accounts.Feedback{StatusCode: http.StatusBadGateway, Err: err})
			continue
		}
		if response.StatusCode == http.StatusUnauthorized && lease.Account.Kind == domain.CredentialCLIOAuth && h.config.RefreshAccount != nil && attempt+1 < h.config.MaxAttempts {
			if _, alreadyRefreshed := refreshed[lease.Account.ID]; !alreadyRefreshed {
				refreshed[lease.Account.ID] = struct{}{}
				refreshErr := h.config.RefreshAccount(ctx, lease.Account.ID)
				_ = lease.Release(context.Background(), accounts.Feedback{StatusCode: 499})
				if refreshErr != nil {
					lastErr = refreshErr
					excluded[lease.Account.ID] = struct{}{}
				}
				continue
			}
		}
		if retryable(response.StatusCode) && attempt+1 < h.config.MaxAttempts {
			lastErr = errors.New(upstreamErrorMessage(response))
			excluded[lease.Account.ID] = struct{}{}
			_ = lease.Release(context.Background(), feedbackFromResponse(response, lastErr))
			continue
		}
		return response, lease, nil
	}
	if lastErr == nil {
		lastErr = accounts.ErrNoAccount
	}
	return nil, nil, lastErr
}

func (h *Handler) readPayload(w http.ResponseWriter, r *http.Request) (json.RawMessage, map[string]any, error) {
	maximum := h.maxBodyBytes()
	r.Body = http.MaxBytesReader(w, r.Body, maximum)
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/form-data") {
		payload, err := multipartPayload(r, maximum)
		if err != nil {
			return nil, nil, err
		}
		body, err := json.Marshal(payload)
		return body, payload, err
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, nil, err
	}
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return nil, nil, err
	}
	return body, payload, nil
}

func (h *Handler) maxBodyBytes() int64 {
	if h.config.MaxBodyBytesFunc != nil {
		if value := h.config.MaxBodyBytesFunc(); value > 0 {
			return value
		}
	}
	return h.config.MaxBodyBytes
}

func multipartPayload(r *http.Request, maxBytes int64) (map[string]any, error) {
	if err := r.ParseMultipartForm(maxBytes); err != nil {
		return nil, err
	}
	result := make(map[string]any)
	for key, values := range r.MultipartForm.Value {
		if len(values) == 1 {
			result[key] = scalar(values[0])
		} else {
			result[key] = values
		}
	}
	for key, headers := range r.MultipartForm.File {
		var files []map[string]any
		for _, header := range headers {
			file, err := header.Open()
			if err != nil {
				return nil, err
			}
			data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
			_ = file.Close()
			if err != nil {
				return nil, err
			}
			if int64(len(data)) > maxBytes {
				return nil, errors.New("uploaded file exceeds configured limit")
			}
			contentType := header.Header.Get("Content-Type")
			if contentType == "" {
				contentType = http.DetectContentType(data)
			}
			files = append(files, map[string]any{"filename": header.Filename, "content_type": contentType, "data": "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(data)})
		}
		result[key] = files
	}
	return result, nil
}

func scalar(value string) any {
	switch strings.ToLower(value) {
	case "true":
		return true
	case "false":
		return false
	default:
		return value
	}
}

func allowsOperation(model domain.ModelSpec, operation upstream.Operation) bool {
	switch operation {
	case upstream.OperationChat:
		return model.Capability == domain.CapabilityChat || model.Capability == domain.CapabilityImage || model.Capability == domain.CapabilityImageEdit || model.Capability == domain.CapabilityVideo
	case upstream.OperationResponses:
		return model.Capability == domain.CapabilityResponses || model.Capability == domain.CapabilityChat
	case upstream.OperationMessages:
		return model.Capability == domain.CapabilityMessages || model.Capability == domain.CapabilityChat
	case upstream.OperationImage:
		return model.Capability == domain.CapabilityImage
	case upstream.OperationImageEdit:
		return model.Capability == domain.CapabilityImageEdit
	case upstream.OperationVideo:
		return model.Capability == domain.CapabilityVideo
	default:
		return false
	}
}

func isConversational(operation upstream.Operation) bool {
	return operation == upstream.OperationChat || operation == upstream.OperationResponses || operation == upstream.OperationMessages
}

func forwardedHeaders(source http.Header) http.Header {
	result := make(http.Header)
	for _, key := range []string{"X-Grok-Conv-Id", "X-Claude-Code-Session-Id", "X-Request-Id"} {
		if value := source.Get(key); value != "" {
			result.Set(key, value)
		}
	}
	return result
}

func retryable(status int) bool {
	return status == 401 || status == 403 || status == 423 || status == 429 || status >= 500
}

func retryAfter(header http.Header) time.Duration {
	value := strings.TrimSpace(header.Get("Retry-After"))
	if value == "" {
		return 0
	}
	if seconds, err := time.ParseDuration(value + "s"); err == nil {
		return seconds
	}
	if deadline, err := http.ParseTime(value); err == nil {
		return time.Until(deadline)
	}
	return 0
}

func feedbackFromResponse(response *upstream.Response, err error) accounts.Feedback {
	if response == nil {
		return accounts.Feedback{StatusCode: http.StatusBadGateway, Err: err}
	}
	if isUpstreamProtectionChallenge(response) {
		return accounts.Feedback{
			RetryAfter: retryAfter(response.Header),
			Quota:      quotaFromHeaders(response.Header, time.Now().UTC()),
			Err:        errors.New(upstreamProtectionChallengeMessage(response.StatusCode)),
		}
	}
	return accounts.Feedback{
		StatusCode: response.StatusCode,
		RetryAfter: retryAfter(response.Header),
		Quota:      quotaFromHeaders(response.Header, time.Now().UTC()),
		Err:        err,
	}
}

func quotaFromHeaders(header http.Header, observedAt time.Time) *domain.QuotaSnapshot {
	quota := domain.QuotaSnapshot{ObservedAt: observedAt}
	seen := false
	seen = readQuotaMetric(header, []string{"X-RateLimit-Limit-Requests", "RateLimit-Limit"}, &quota.RequestsLimit, &quota.RequestsUnlimited) || seen
	seen = readQuotaMetric(header, []string{"X-RateLimit-Remaining-Requests", "RateLimit-Remaining"}, &quota.RequestsRemaining, nil) || seen
	seen = readQuotaMetric(header, []string{"X-RateLimit-Limit-Tokens"}, &quota.TokensLimit, &quota.TokensUnlimited) || seen
	seen = readQuotaMetric(header, []string{"X-RateLimit-Remaining-Tokens"}, &quota.TokensRemaining, nil) || seen
	requestReset, requestResetSeen := quotaResetFromHeaders(header, []string{"X-RateLimit-Reset-Requests"}, observedAt)
	tokenReset, tokenResetSeen := quotaResetFromHeaders(header, []string{"X-RateLimit-Reset-Tokens"}, observedAt)
	commonReset, commonResetSeen := quotaResetFromHeaders(header, []string{"X-RateLimit-Reset", "RateLimit-Reset"}, observedAt)
	seen = seen || requestResetSeen || tokenResetSeen || commonResetSeen
	requestExhausted := !quota.RequestsUnlimited && quota.RequestsLimit > 0 && quota.RequestsRemaining <= 0
	tokenExhausted := !quota.TokensUnlimited && quota.TokensLimit > 0 && quota.TokensRemaining <= 0
	switch {
	case requestExhausted && tokenExhausted:
		quota.ResetAt = laterReset(firstReset(requestReset, commonReset), firstReset(tokenReset, commonReset))
	case requestExhausted:
		quota.ResetAt = firstReset(requestReset, commonReset)
	case tokenExhausted:
		quota.ResetAt = firstReset(tokenReset, commonReset)
	default:
		quota.ResetAt = laterReset(laterReset(requestReset, tokenReset), commonReset)
	}
	if !seen {
		return nil
	}
	return &quota
}

func quotaResetFromHeaders(header http.Header, names []string, observedAt time.Time) (*time.Time, bool) {
	var result *time.Time
	seen := false
	for _, name := range names {
		value := strings.TrimSpace(header.Get(name))
		if value == "" {
			continue
		}
		seen = true
		if reset, ok := parseQuotaReset(value, observedAt); ok && (result == nil || reset.After(*result)) {
			result = &reset
		}
	}
	return result, seen
}

func firstReset(primary, fallback *time.Time) *time.Time {
	if primary != nil {
		return primary
	}
	return fallback
}

func laterReset(left, right *time.Time) *time.Time {
	if left == nil {
		return right
	}
	if right == nil || left.After(*right) {
		return left
	}
	return right
}

func readQuotaMetric(header http.Header, names []string, destination *int64, unlimited *bool) bool {
	for _, name := range names {
		value := strings.TrimSpace(header.Get(name))
		if value == "" {
			continue
		}
		lower := strings.ToLower(value)
		if unlimited != nil && (lower == "unlimited" || lower == "infinite" || lower == "inf") {
			*unlimited = true
			return true
		}
		if parsed, err := strconv.ParseInt(value, 10, 64); err == nil {
			*destination = max(int64(0), parsed)
			return true
		}
		return true
	}
	return false
}

func parseQuotaReset(value string, now time.Time) (time.Time, bool) {
	if duration, err := time.ParseDuration(value); err == nil {
		return now.Add(duration), true
	}
	if number, err := strconv.ParseInt(value, 10, 64); err == nil {
		if number > 1_000_000_000 {
			return time.Unix(number, 0), true
		}
		return now.Add(time.Duration(number) * time.Second), true
	}
	if reset, err := http.ParseTime(value); err == nil {
		return reset, true
	}
	if reset, err := time.Parse(time.RFC3339, value); err == nil {
		return reset, true
	}
	return time.Time{}, false
}

func upstreamErrorMessage(response *upstream.Response) string {
	if response == nil {
		return "upstream request failed"
	}
	if isUpstreamProtectionChallenge(response) {
		return upstreamProtectionChallengeMessage(response.StatusCode)
	}
	if len(response.Body) > 0 {
		var payload map[string]any
		if json.Unmarshal(response.Body, &payload) == nil {
			if value, _ := payload["message"].(string); value != "" {
				return value
			}
			if nested, ok := payload["error"].(map[string]any); ok {
				if value, _ := nested["message"].(string); value != "" {
					return value
				}
			}
		}
		return strings.TrimSpace(string(response.Body))
	}
	return fmt.Sprintf("upstream returned status %d", response.StatusCode)
}

func upstreamProtectionChallengeMessage(statusCode int) string {
	return fmt.Sprintf("upstream protection challenge returned HTTP %d; check the account proxy and retry", statusCode)
}

func isUpstreamProtectionChallenge(response *upstream.Response) bool {
	if response == nil || response.StatusCode != http.StatusForbidden {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(response.Header.Get("CF-Mitigated")), "challenge") {
		return true
	}
	lowerBody := strings.ToLower(string(response.Body))
	if strings.Contains(lowerBody, "challenges.cloudflare.com") ||
		strings.Contains(lowerBody, "cf-chl-") ||
		strings.Contains(lowerBody, "<title>just a moment...</title>") {
		return true
	}
	if !strings.Contains(strings.ToLower(response.Header.Get("Server")), "cloudflare") {
		return false
	}
	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	trimmedBody := bytes.TrimSpace(response.Body)
	return strings.Contains(contentType, "text/html") || bytes.HasPrefix(bytes.ToLower(trimmedBody), []byte("<!doctype html")) || bytes.HasPrefix(bytes.ToLower(trimmedBody), []byte("<html"))
}

func mapStatus(status int) int {
	if status == 400 || status == 401 || status == 403 || status == 404 || status == 429 {
		return status
	}
	return http.StatusBadGateway
}

func errorType(status int) string {
	if status == 429 {
		return "rate_limit_error"
	}
	if status >= 400 && status < 500 {
		return "invalid_request_error"
	}
	return "server_error"
}

func writeError(w http.ResponseWriter, status int, message, kind, code, parameter string) {
	errorValue := map[string]any{"message": message, "type": kind, "code": code}
	if parameter != "" {
		errorValue["param"] = parameter
	}
	writeJSON(w, status, map[string]any{"error": errorValue})
}

func writeOperationError(w http.ResponseWriter, operation upstream.Operation, status int, message, kind, code, parameter string) {
	if operation != upstream.OperationMessages {
		writeError(w, status, message, kind, code, parameter)
		return
	}
	errorKind := "api_error"
	switch status {
	case http.StatusBadRequest:
		errorKind = "invalid_request_error"
	case http.StatusUnauthorized:
		errorKind = "authentication_error"
	case http.StatusForbidden:
		errorKind = "permission_error"
	case http.StatusNotFound:
		errorKind = "not_found_error"
	case http.StatusRequestEntityTooLarge:
		errorKind = "request_too_large"
	case http.StatusTooManyRequests:
		errorKind = "rate_limit_error"
	case http.StatusServiceUnavailable:
		errorKind = "overloaded_error"
	}
	writeJSON(w, status, map[string]any{"type": "error", "error": map[string]any{"type": errorKind, "message": message}})
}

func operationForPath(path string) upstream.Operation {
	switch strings.TrimSuffix(path, "/") {
	case "/v1/chat/completions":
		return upstream.OperationChat
	case "/v1/responses":
		return upstream.OperationResponses
	case "/v1/messages":
		return upstream.OperationMessages
	case "/v1/images/generations":
		return upstream.OperationImage
	case "/v1/images/edits":
		return upstream.OperationImageEdit
	case "/v1/videos", "/v1/videos/generations":
		return upstream.OperationVideo
	default:
		return ""
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

var _ multipart.File
