package upstream

import (
	"bufio"
	"bytes"
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/domain"
)

type HTTPClientConfig struct {
	Client             *http.Client
	BackgroundContext  context.Context
	GrokBaseURL        string
	ImagineURL         string
	ConsoleBaseURL     string
	CLIBaseURL         string
	RequestTimeout     time.Duration
	VideoTimeout       time.Duration
	RequestTimeoutFunc func() time.Duration
	MaxResponseSize    int64
	MaxProxyClients    int
}

type HTTPClient struct {
	config       HTTPClientConfig
	client       *http.Client
	proxyMu      sync.Mutex
	proxyClients map[string]*list.Element
	proxyLRU     *list.List
	videoMu      sync.RWMutex
	videoJobs    map[string]*grokVideoJob
}

type proxyClientEntry struct {
	key       string
	client    *http.Client
	transport *http.Transport
}

func NewHTTPClient(config HTTPClientConfig) *HTTPClient {
	if config.GrokBaseURL == "" {
		config.GrokBaseURL = "https://grok.com"
	}
	if config.ImagineURL == "" {
		config.ImagineURL = "wss://grok.com/ws/imagine/listen"
	}
	if config.ConsoleBaseURL == "" {
		config.ConsoleBaseURL = "https://console.x.ai/v1"
	}
	if config.CLIBaseURL == "" {
		config.CLIBaseURL = "https://cli-chat-proxy.grok.com/v1"
	}
	if config.RequestTimeout <= 0 {
		config.RequestTimeout = 120 * time.Second
	}
	if config.VideoTimeout <= 0 {
		config.VideoTimeout = 10 * time.Minute
	}
	if config.BackgroundContext == nil {
		config.BackgroundContext = context.Background()
	}
	if config.MaxResponseSize <= 0 {
		config.MaxResponseSize = 32 << 20
	}
	if config.MaxProxyClients <= 0 {
		config.MaxProxyClients = 256
	}
	client := config.Client
	if client == nil {
		transport, err := NewTransport("")
		if err != nil {
			panic(err)
		}
		client = &http.Client{Transport: transport}
	}
	return &HTTPClient{config: config, client: client, proxyClients: make(map[string]*list.Element), proxyLRU: list.New(), videoJobs: make(map[string]*grokVideoJob)}
}

func (c *HTTPClient) Do(ctx context.Context, input Request) (*Response, error) {
	if input.CredentialKind == domain.CredentialGrokSSO && input.Operation == OperationVideo {
		return c.startGrokVideo(ctx, input)
	}
	if input.CredentialKind == domain.CredentialGrokSSO && input.Operation == OperationVideoStatus {
		return c.grokVideoStatus(input.VideoID), nil
	}
	if useImagineWebSocket(input) {
		return c.doImagine(ctx, input)
	}
	if input.Operation == OperationImageEdit && input.CredentialKind == domain.CredentialGrokSSO {
		return c.doGrokImageEdit(ctx, input)
	}
	timeout := c.requestTimeout()
	reverseToolNames := requestToolReverseNames(input)
	endpoint, err := c.endpoint(input)
	if err != nil {
		return nil, err
	}
	method := http.MethodPost
	var requestBody io.Reader
	if input.Operation == OperationVideoStatus {
		method = http.MethodGet
	} else {
		payload, payloadErr := preparePayload(input)
		if payloadErr != nil {
			return nil, payloadErr
		}
		requestBody = bytes.NewReader(payload)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, requestBody)
	if err != nil {
		return nil, err
	}
	if method != http.MethodGet {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Accept", "text/event-stream, application/json")
	copyHeader(request.Header, input.Headers)
	adapter, err := AdapterFor(input.CredentialKind)
	if err != nil {
		return nil, err
	}
	if err := adapter.Apply(request, input.Credentials); err != nil {
		return nil, err
	}

	client := c.client
	if strings.TrimSpace(input.ProxyURL) != "" {
		var transportErr error
		client, transportErr = c.clientForProxy(input.ProxyURL)
		if transportErr != nil {
			return nil, fmt.Errorf("configure account proxy: %w", transportErr)
		}
	}
	requestClient := *client
	requestClient.Timeout = timeout
	response, err := requestClient.Do(request)
	if err != nil {
		return nil, err
	}
	result := &Response{StatusCode: response.StatusCode, Header: response.Header.Clone()}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer response.Body.Close()
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 2<<20))
		if readErr != nil {
			return nil, readErr
		}
		result.Body = body
		return result, nil
	}
	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	if input.Stream || strings.Contains(contentType, "text/event-stream") {
		result.Events = streamEventsWithToolNames(ctx, response.Body, reverseToolNames)
		return result, nil
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, c.config.MaxResponseSize+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > c.config.MaxResponseSize {
		return nil, errors.New("upstream response exceeds configured limit")
	}
	result.Body = body
	result.Events = Events(parseNonStreamWithToolNames(body, reverseToolNames)...)
	return result, nil
}

func (c *HTTPClient) requestTimeout() time.Duration {
	if c.config.RequestTimeoutFunc != nil {
		if value := c.config.RequestTimeoutFunc(); value > 0 {
			return value
		}
	}
	return c.config.RequestTimeout
}

func (c *HTTPClient) clientForProxy(raw string) (*http.Client, error) {
	key, err := normalizeProxyURL(raw)
	if err != nil {
		return nil, err
	}
	c.proxyMu.Lock()
	if element := c.proxyClients[key]; element != nil {
		c.proxyLRU.MoveToFront(element)
		client := element.Value.(*proxyClientEntry).client
		c.proxyMu.Unlock()
		return client, nil
	}
	c.proxyMu.Unlock()

	transport, err := NewTransport(key)
	if err != nil {
		return nil, err
	}
	entry := &proxyClientEntry{key: key, client: &http.Client{Transport: transport}, transport: transport}

	c.proxyMu.Lock()
	defer c.proxyMu.Unlock()
	if element := c.proxyClients[key]; element != nil {
		transport.CloseIdleConnections()
		c.proxyLRU.MoveToFront(element)
		return element.Value.(*proxyClientEntry).client, nil
	}
	element := c.proxyLRU.PushFront(entry)
	c.proxyClients[key] = element
	for c.proxyLRU.Len() > c.config.MaxProxyClients {
		oldest := c.proxyLRU.Back()
		if oldest == nil {
			break
		}
		removed := oldest.Value.(*proxyClientEntry)
		delete(c.proxyClients, removed.key)
		c.proxyLRU.Remove(oldest)
		removed.transport.CloseIdleConnections()
	}
	return entry.client, nil
}

func normalizeProxyURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Hostname() == "" {
		return "", errors.New("proxy URL must include a scheme and host")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	switch parsed.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return "", fmt.Errorf("unsupported proxy scheme %q", parsed.Scheme)
	}
	if (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("proxy URL must not contain a path, query, or fragment")
	}
	host := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if port == "" {
		switch parsed.Scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		default:
			port = "1080"
		}
	}
	parsed.Host = net.JoinHostPort(host, port)
	parsed.Path, parsed.RawPath = "", ""
	return parsed.String(), nil
}

// CloseIdleConnections releases idle direct and cached proxy connections.
func (c *HTTPClient) CloseIdleConnections() {
	if c == nil {
		return
	}
	c.client.CloseIdleConnections()
	c.proxyMu.Lock()
	defer c.proxyMu.Unlock()
	for _, element := range c.proxyClients {
		element.Value.(*proxyClientEntry).transport.CloseIdleConnections()
	}
}

func (c *HTTPClient) endpoint(request Request) (string, error) {
	base := request.Credentials.BaseURL
	if base == "" {
		switch request.CredentialKind {
		case domain.CredentialGrokSSO:
			base = c.config.GrokBaseURL
		case domain.CredentialConsoleSSO:
			base = c.config.ConsoleBaseURL
		case domain.CredentialCLIOAuth:
			base = c.config.CLIBaseURL
		default:
			return "", errors.New("unsupported upstream credential kind")
		}
	}
	base = strings.TrimRight(base, "/")
	if _, err := url.ParseRequestURI(base); err != nil {
		return "", fmt.Errorf("invalid upstream base URL: %w", err)
	}
	if request.CredentialKind == domain.CredentialGrokSSO {
		switch request.Operation {
		case OperationVideo:
			return base + "/rest/media/post/create", nil
		case OperationVideoStatus:
			return videoStatusEndpoint(base, request.StatusURL, request.VideoID)
		default:
			return base + "/rest/app-chat/conversations/new", nil
		}
	}
	switch request.Operation {
	case OperationImage:
		return base + "/images/generations", nil
	case OperationImageEdit:
		return base + "/images/edits", nil
	case OperationVideo:
		return base + "/videos/generations", nil
	case OperationVideoStatus:
		return videoStatusEndpoint(base, request.StatusURL, request.VideoID)
	default:
		return base + "/responses", nil
	}
}

func videoStatusEndpoint(base, rawStatusURL, videoID string) (string, error) {
	baseURL, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("invalid upstream base URL: %w", err)
	}
	if strings.TrimSpace(rawStatusURL) != "" {
		statusURL, parseErr := url.Parse(strings.TrimSpace(rawStatusURL))
		if parseErr != nil {
			return "", fmt.Errorf("invalid video status URL: %w", parseErr)
		}
		statusURL = baseURL.ResolveReference(statusURL)
		if statusURL.User != nil || !strings.EqualFold(statusURL.Scheme, baseURL.Scheme) || !strings.EqualFold(statusURL.Host, baseURL.Host) {
			return "", errors.New("video status URL must use the upstream origin")
		}
		statusURL.Fragment = ""
		return statusURL.String(), nil
	}
	videoID = strings.TrimSpace(videoID)
	if videoID == "" {
		return "", errors.New("video ID is required for status query")
	}
	return base + "/videos/" + url.PathEscape(videoID), nil
}

func preparePayload(request Request) ([]byte, error) {
	var payload map[string]any
	if len(request.Body) > 0 {
		decoder := json.NewDecoder(bytes.NewReader(request.Body))
		decoder.UseNumber()
		if err := decoder.Decode(&payload); err != nil {
			return nil, fmt.Errorf("decode gateway payload: %w", err)
		}
	} else {
		payload = make(map[string]any)
	}
	model := request.UpstreamModel
	if model == "" {
		model = request.Model
	}
	if request.CredentialKind == domain.CredentialGrokSSO {
		return json.Marshal(prepareGrokWebPayload(request, payload, model))
	}
	if request.Operation == OperationImage || request.Operation == OperationImageEdit || request.Operation == OperationVideo {
		payload["model"] = model
		return json.Marshal(payload)
	}
	result := prepareResponsesPayload(request, payload, model)
	return json.Marshal(result)
}

func responseInput(operation Operation, payload map[string]any) any {
	switch operation {
	case OperationChat:
		return payload["messages"]
	case OperationMessages:
		return payload["messages"]
	default:
		return payload["input"]
	}
}

func extractPrompt(operation Operation, payload map[string]any) string {
	var value any
	switch operation {
	case OperationResponses:
		value = payload["input"]
	case OperationImage, OperationImageEdit, OperationVideo:
		value = payload["prompt"]
	default:
		value = payload["messages"]
	}
	var parts []string
	collectText(value, &parts)
	if system, ok := payload["system"]; ok {
		parts = append([]string{systemText(system)}, parts...)
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func collectText(value any, parts *[]string) {
	switch value := value.(type) {
	case string:
		if strings.TrimSpace(value) != "" {
			*parts = append(*parts, value)
		}
	case []any:
		for _, item := range value {
			collectText(item, parts)
		}
	case map[string]any:
		if role, _ := value["role"].(string); role != "" {
			before := len(*parts)
			collectText(value["content"], parts)
			if len(*parts) > before {
				(*parts)[before] = "[" + role + "] " + (*parts)[before]
			}
			return
		}
		for _, key := range []string{"text", "input_text", "prompt", "content"} {
			if item, ok := value[key]; ok {
				collectText(item, parts)
				return
			}
		}
	}
}

func systemText(value any) string {
	var parts []string
	collectText(value, &parts)
	return strings.Join(parts, "\n")
}

func grokMode(model string) string {
	lower := strings.ToLower(model)
	switch lower {
	case "auto", "fast", "expert", "heavy", "grok-420-computer-use-sa":
		return lower
	}
	switch {
	case strings.Contains(lower, "heavy"), strings.Contains(lower, "multi-agent"):
		return "heavy"
	case strings.Contains(lower, "reasoning"), strings.Contains(lower, "expert"):
		return "expert"
	case strings.Contains(lower, "fast"), strings.Contains(lower, "non-reasoning"):
		return "fast"
	default:
		return "auto"
	}
}

func streamEvents(ctx context.Context, body io.ReadCloser) <-chan Event {
	return streamEventsWithToolNames(ctx, body, nil)
}

func streamEventsWithToolNames(ctx context.Context, body io.ReadCloser, reverseToolNames map[string]string) <-chan Event {
	result := make(chan Event, 16)
	streamCtx, cancel := context.WithCancel(ctx)
	var closeOnce sync.Once
	closeBody := func() {
		closeOnce.Do(func() { _ = body.Close() })
	}
	go func() {
		<-streamCtx.Done()
		closeBody()
	}()
	go func() {
		defer close(result)
		defer cancel()
		defer closeBody()
		send := func(event Event) bool {
			if streamCtx.Err() != nil {
				return false
			}
			select {
			case result <- event:
				return true
			case <-streamCtx.Done():
				return false
			}
		}
		scanner := bufio.NewScanner(body)
		scanner.Buffer(make([]byte, 64<<10), 8<<20)
		parser := newSSEParserWithToolNames(reverseToolNames)
		eventName := ""
		dataLines := make([]string, 0, 1)
		dispatch := func() bool {
			if len(dataLines) == 0 {
				eventName = ""
				return true
			}
			for _, event := range parser.parse(eventName, []byte(strings.Join(dataLines, "\n"))) {
				if !send(event) {
					return false
				}
			}
			eventName = ""
			dataLines = dataLines[:0]
			return true
		}
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case strings.HasPrefix(line, "event:"):
				eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			case strings.HasPrefix(strings.TrimSpace(line), "{"):
				if !dispatch() {
					return
				}
				for _, event := range parser.parse(eventName, []byte(strings.TrimSpace(line))) {
					if !send(event) {
						return
					}
				}
			}
			if line == "" && !dispatch() {
				return
			}
		}
		if !dispatch() {
			return
		}
		if err := scanner.Err(); err != nil && streamCtx.Err() == nil {
			_ = send(Event{Kind: EventError, Error: err.Error()})
		}
	}()
	return result
}

func parseSSEEvent(eventName string, data []byte) []Event {
	return newSSEParser().parse(eventName, data)
}

func parseNonStream(body []byte) []Event {
	return parseNonStreamWithToolNames(body, nil)
}

func parseNonStreamWithToolNames(body []byte, reverseToolNames map[string]string) []Event {
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil {
		return []Event{{Kind: EventError, Error: "invalid JSON response"}}
	}
	events := responseObjectEvents(payload, body, reverseToolNames)
	events = append(events, usageEvents(payload)...)
	events = append(events, Event{Kind: EventDone})
	return events
}

func nonStreamText(payload map[string]any) string {
	if value := stringValue(payload, "output_text", "text"); value != "" {
		return value
	}
	if choices, ok := payload["choices"].([]any); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]any); ok {
			if message, ok := choice["message"].(map[string]any); ok {
				return stringValue(message, "content")
			}
		}
	}
	var parts []string
	collectTypedText(payload["output"], &parts)
	return strings.Join(parts, "")
}

func collectTypedText(value any, parts *[]string) {
	switch value := value.(type) {
	case []any:
		for _, child := range value {
			collectTypedText(child, parts)
		}
	case map[string]any:
		if text := stringValue(value, "text"); text != "" {
			*parts = append(*parts, text)
		}
		collectTypedText(value["content"], parts)
	}
}

func chatDelta(payload map[string]any) string {
	choices, ok := payload["choices"].([]any)
	if !ok || len(choices) == 0 {
		return ""
	}
	choice, _ := choices[0].(map[string]any)
	delta, _ := choice["delta"].(map[string]any)
	return stringValue(delta, "content", "reasoning_content")
}

func usageEvents(payload map[string]any) []Event {
	usage, _ := payload["usage"].(map[string]any)
	if usage == nil {
		if response, ok := payload["response"].(map[string]any); ok {
			usage, _ = response["usage"].(map[string]any)
		}
	}
	if usage == nil {
		return nil
	}
	result := Usage{InputTokens: int64Value(usage, "input_tokens", "prompt_tokens"), OutputTokens: int64Value(usage, "output_tokens", "completion_tokens")}
	details, _ := usage["input_tokens_details"].(map[string]any)
	if details == nil {
		details, _ = usage["prompt_tokens_details"].(map[string]any)
	}
	if details != nil {
		result.CachedTokens = int64Value(details, "cached_tokens", "cache_read_tokens")
	}
	if result.CachedTokens == 0 {
		result.CachedTokens = int64Value(usage, "cache_read_input_tokens", "cached_tokens")
	}
	return []Event{{Kind: EventUsage, Usage: result}}
}

func mediaEvents(payload map[string]any, raw []byte) []Event {
	var result []Event
	walkJSON(payload, func(key string, value any) {
		text, ok := value.(string)
		if !ok || !strings.HasPrefix(text, "http") {
			return
		}
		lower := strings.ToLower(key + " " + text)
		switch {
		case strings.Contains(lower, "video") || strings.HasSuffix(strings.Split(text, "?")[0], ".mp4"):
			result = append(result, Event{Kind: EventVideo, URL: text, Raw: append([]byte(nil), raw...)})
		case strings.Contains(lower, "image") || strings.Contains(lower, "thumbnail") || strings.HasSuffix(strings.Split(text, "?")[0], ".png") || strings.HasSuffix(strings.Split(text, "?")[0], ".jpg"):
			result = append(result, Event{Kind: EventImage, URL: text, Raw: append([]byte(nil), raw...)})
		}
	})
	return result
}

func walkJSON(value any, visit func(string, any)) {
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			visit(key, child)
			walkJSON(child, visit)
		}
	case []any:
		for _, child := range value {
			walkJSON(child, visit)
		}
	}
}

func copyHeader(destination, source http.Header) {
	for key, values := range source {
		for _, value := range values {
			destination.Add(key, value)
		}
	}
}

func copyIfPresent(destination, source map[string]any, keys ...string) {
	for _, key := range keys {
		if value, ok := source[key]; ok {
			destination[key] = value
		}
	}
}

func stringValue(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := payload[key].(string); ok {
			return value
		}
	}
	return ""
}

func nestedString(payload map[string]any, keys ...string) string {
	var current any = payload
	for _, key := range keys {
		mapping, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = mapping[key]
	}
	result, _ := current.(string)
	return result
}

func intValue(value any) int {
	switch value := value.(type) {
	case int:
		return value
	case int32:
		return int(value)
	case int64:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		result, _ := value.Int64()
		return int(result)
	case string:
		result, _ := strconv.Atoi(strings.TrimSpace(value))
		return result
	default:
		return 0
	}
}

func int64Value(payload map[string]any, keys ...string) int64 {
	for _, key := range keys {
		if value, ok := payload[key]; ok {
			return int64(intValue(value))
		}
	}
	return 0
}

func errorText(payload map[string]any) string {
	if value := stringValue(payload, "message", "error"); value != "" {
		return value
	}
	if nested, ok := payload["error"].(map[string]any); ok {
		return stringValue(nested, "message", "error")
	}
	return "upstream stream error"
}
