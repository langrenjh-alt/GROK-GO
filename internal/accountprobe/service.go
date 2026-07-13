package accountprobe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/accounts"
	"github.com/langrenjh-alt/GROK-GO/internal/domain"
	"github.com/langrenjh-alt/GROK-GO/internal/upstream"
)

const (
	defaultTimeout     = 45 * time.Second
	defaultParallelism = 8
	maximumModelLength = 200
)

type AccountReader interface {
	GetAccount(context.Context, string) (*domain.Account, error)
}

type ProxyURLResolver func(context.Context, string) (string, error)

type Config struct {
	Accounts    *accounts.Pool
	Reader      AccountReader
	Upstream    upstream.Client
	ProxyURL    ProxyURLResolver
	Timeout     time.Duration
	Parallelism int
}

type Service struct {
	accounts    *accounts.Pool
	reader      AccountReader
	upstream    upstream.Client
	proxyURL    ProxyURLResolver
	timeout     time.Duration
	parallelism int
}

type Input struct {
	Model string `json:"model,omitempty"`
}

type Result struct {
	AccountID   string          `json:"account_id"`
	Success     bool            `json:"success"`
	StatusCode  int             `json:"status_code,omitempty"`
	DurationMS  int64           `json:"duration_ms"`
	Model       string          `json:"model"`
	Message     string          `json:"message"`
	CompletedAt time.Time       `json:"completed_at"`
	Account     *domain.Account `json:"account,omitempty"`
}

type BatchResult struct {
	Total     int      `json:"total"`
	Succeeded int      `json:"succeeded"`
	Failed    int      `json:"failed"`
	Items     []Result `json:"items"`
}

func New(config Config) (*Service, error) {
	if config.Accounts == nil || config.Reader == nil || config.Upstream == nil {
		return nil, errors.New("account probe requires an account pool, account reader, and upstream client")
	}
	if config.Timeout <= 0 {
		config.Timeout = defaultTimeout
	}
	if config.Parallelism <= 0 {
		config.Parallelism = defaultParallelism
	}
	return &Service{
		accounts: config.Accounts, reader: config.Reader, upstream: config.Upstream,
		proxyURL: config.ProxyURL, timeout: config.Timeout, parallelism: config.Parallelism,
	}, nil
}

// Probe sends a small real request through the selected account and its bound
// proxy. A 2xx status is accepted only after a parseable upstream event/body is
// observed. The resulting protocol feedback is persisted before this returns.
func (s *Service) Probe(ctx context.Context, accountID string, input Input) (Result, error) {
	started := time.Now()
	result := Result{AccountID: strings.TrimSpace(accountID)}
	if result.AccountID == "" {
		return result, errors.New("account ID is required")
	}
	probeContext, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	if _, err := s.reader.GetAccount(probeContext, result.AccountID); err != nil {
		return result, err
	}

	lease, err := s.accounts.AcquireForProbe(probeContext, result.AccountID)
	if errors.Is(err, accounts.ErrNoAccount) {
		if reloadErr := s.accounts.Reload(probeContext); reloadErr != nil {
			return result, reloadErr
		}
		lease, err = s.accounts.AcquireForProbe(probeContext, result.AccountID)
	}
	if err != nil {
		return result, err
	}

	result.Model, err = probeModel(lease.Account, input.Model)
	if err != nil {
		_ = lease.Release(context.Background(), accounts.Feedback{StatusCode: http.StatusBadRequest})
		return result, err
	}
	proxyURL := ""
	if lease.Account.ProxyID != "" && s.proxyURL != nil {
		proxyURL, err = s.proxyURL(probeContext, lease.Account.ProxyID)
		if err != nil {
			return s.complete(started, lease, result, 0, fmt.Errorf("resolve account proxy: %w", err), nil)
		}
	}
	payload, err := json.Marshal(map[string]any{
		"input":             "Reply with OK.",
		"max_output_tokens": 1,
		"stream":            false,
	})
	if err != nil {
		return s.complete(started, lease, result, 0, err, nil)
	}

	response, requestErr := s.upstream.Do(probeContext, upstream.Request{
		Operation:      upstream.OperationResponses,
		Model:          result.Model,
		UpstreamModel:  result.Model,
		CredentialKind: lease.Account.Kind,
		Credentials:    lease.Credentials,
		ProxyURL:       proxyURL,
		Body:           payload,
		Stream:         false,
	})
	if requestErr != nil {
		return s.complete(started, lease, result, 0, fmt.Errorf("upstream request failed: %w", requestErr), nil)
	}
	if response == nil {
		return s.complete(started, lease, result, 0, errors.New("upstream returned no response"), nil)
	}
	result.StatusCode = response.StatusCode
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return s.complete(started, lease, result, response.StatusCode, upstreamResponseError(response), response.Header)
	}
	if err := validateResponse(probeContext, response); err != nil {
		return s.complete(started, lease, result, response.StatusCode, err, response.Header)
	}
	return s.complete(started, lease, result, response.StatusCode, nil, response.Header)
}

func (s *Service) ProbeMany(ctx context.Context, accountIDs []string, input Input) BatchResult {
	result := BatchResult{Total: len(accountIDs), Items: make([]Result, len(accountIDs))}
	if len(accountIDs) == 0 {
		return result
	}
	type job struct {
		index int
		id    string
	}
	jobs := make(chan job)
	workers := min(s.parallelism, len(accountIDs))
	var wait sync.WaitGroup
	wait.Add(workers)
	for range workers {
		go func() {
			defer wait.Done()
			for item := range jobs {
				probe, err := s.Probe(ctx, item.id, input)
				if err != nil {
					probe.AccountID = item.id
					probe.Message = cleanMessage(err.Error())
					probe.CompletedAt = time.Now().UTC()
				}
				result.Items[item.index] = probe
			}
		}()
	}
	for index, id := range accountIDs {
		jobs <- job{index: index, id: id}
	}
	close(jobs)
	wait.Wait()
	for _, item := range result.Items {
		if item.Success {
			result.Succeeded++
		}
	}
	result.Failed = result.Total - result.Succeeded
	return result
}

func (s *Service) complete(started time.Time, lease *accounts.Lease, result Result, statusCode int, probeErr error, header http.Header) (Result, error) {
	result.StatusCode = statusCode
	result.DurationMS = max(0, time.Since(started).Milliseconds())
	result.CompletedAt = time.Now().UTC()
	result.Success = probeErr == nil
	if probeErr == nil {
		result.Message = "Upstream request completed successfully."
	} else {
		result.Message = cleanMessage(probeErr.Error())
	}
	feedbackStatus := statusCode
	if feedbackStatus == 0 {
		feedbackStatus = http.StatusBadGateway
	}
	feedback := accounts.HTTPFeedback(feedbackStatus, header, probeErr, result.CompletedAt)
	releaseContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := lease.Release(releaseContext, feedback); err != nil {
		return result, fmt.Errorf("persist account probe feedback: %w", err)
	}
	account, err := s.reader.GetAccount(releaseContext, result.AccountID)
	if err != nil {
		return result, fmt.Errorf("reload probed account: %w", err)
	}
	result.Account = account
	return result, nil
}

func probeModel(account domain.Account, requested string) (string, error) {
	model := strings.TrimSpace(requested)
	if model == "" {
		switch account.Kind {
		case domain.CredentialCLIOAuth:
			model = "grok-4.5"
		case domain.CredentialConsoleSSO:
			model = "grok-4.3"
		case domain.CredentialGrokSSO:
			model = "grok-4.20-fast"
		default:
			return "", errors.New("unsupported account credential kind")
		}
	}
	if len(model) > maximumModelLength || strings.ContainsAny(model, "\r\n\x00") {
		return "", errors.New("probe model is invalid")
	}
	return model, nil
}

func validateResponse(ctx context.Context, response *upstream.Response) error {
	if response.Events == nil {
		body := bytes.TrimSpace(response.Body)
		if len(body) == 0 {
			return errors.New("upstream returned an empty response")
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			return errors.New("upstream returned an invalid JSON response")
		}
		if value, ok := payload["error"]; ok && value != nil {
			return errors.New(responseErrorValue(value))
		}
		return nil
	}
	seen := false
	for {
		select {
		case <-ctx.Done():
			drainEvents(response.Events)
			return fmt.Errorf("upstream response timed out: %w", ctx.Err())
		case event, ok := <-response.Events:
			if !ok {
				if !seen {
					return errors.New("upstream returned no parseable response events")
				}
				return nil
			}
			switch event.Kind {
			case upstream.EventError:
				drainEvents(response.Events)
				message := strings.TrimSpace(event.Error)
				if message == "" {
					message = "upstream returned an error event"
				}
				return errors.New(message)
			case upstream.EventTextDelta, upstream.EventReasoningDelta, upstream.EventToolCall,
				upstream.EventImage, upstream.EventVideo, upstream.EventUsage, upstream.EventDone:
				seen = true
			}
		}
	}
}

func drainEvents(events <-chan upstream.Event) {
	go func() {
		for range events {
		}
	}()
}

func upstreamResponseError(response *upstream.Response) error {
	detail := ""
	body := bytes.TrimSpace(response.Body)
	if len(body) > 0 {
		var payload map[string]any
		if json.Unmarshal(body, &payload) == nil {
			if value, ok := payload["error"]; ok {
				detail = responseErrorValue(value)
			} else if value, ok := payload["message"]; ok {
				detail = fmt.Sprint(value)
			}
		}
		if detail == "" {
			detail = string(body)
		}
	}
	if detail == "" {
		detail = http.StatusText(response.StatusCode)
	}
	return fmt.Errorf("upstream returned HTTP %d: %s", response.StatusCode, cleanMessage(detail))
}

func responseErrorValue(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case map[string]any:
		for _, key := range []string{"message", "detail", "error", "code"} {
			if text, ok := value[key].(string); ok && strings.TrimSpace(text) != "" {
				return text
			}
		}
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func cleanMessage(value string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	const maximum = 1000
	runes := []rune(value)
	if len(runes) > maximum {
		return string(runes[:maximum]) + "..."
	}
	return value
}
