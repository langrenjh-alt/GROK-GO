package upstream

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

const maxImageEditReferences = 7

type uploadedReference struct {
	url string
	err error
}

func (c *HTTPClient) doGrokImageEdit(ctx context.Context, input Request) (*Response, error) {
	var source map[string]any
	decoder := json.NewDecoder(bytes.NewReader(input.Body))
	decoder.UseNumber()
	if err := decoder.Decode(&source); err != nil {
		return nil, fmt.Errorf("decode image edit request: %w", err)
	}
	prompt := strings.TrimSpace(stringValue(source, "prompt"))
	if prompt == "" {
		return nil, errors.New("image edit prompt is required")
	}
	references := imageReferences(source)
	if len(references) == 0 {
		return nil, errors.New("image edit requires at least one image")
	}
	if len(references) > maxImageEditReferences {
		references = references[len(references)-maxImageEditReferences:]
	}
	base, err := c.grokBase(input)
	if err != nil {
		return nil, err
	}

	resolved, response, err := c.uploadEditReferences(ctx, input, base, references)
	if err != nil || response != nil {
		return response, err
	}
	postResponse, err := c.doGrokJSON(ctx, input, http.MethodPost, base+"/rest/media/post/create", map[string]any{"mediaType": "MEDIA_POST_TYPE_IMAGE", "prompt": prompt}, 2<<20)
	if err != nil {
		return nil, err
	}
	if postResponse.StatusCode < 200 || postResponse.StatusCode >= 300 {
		return postResponse, nil
	}
	var postPayload map[string]any
	if json.Unmarshal(postResponse.Body, &postPayload) != nil {
		return nil, errors.New("decode image edit media post response")
	}
	post, _ := postPayload["post"].(map[string]any)
	parentID := stringValue(post, "id")
	if parentID == "" {
		return nil, errors.New("image edit media post returned no id")
	}
	if value := strings.TrimSpace(stringValue(post, "originalPrompt", "prompt")); value != "" {
		prompt = value
	}

	editSource := cloneMapValue(source)
	for _, key := range []string{"image", "images", "image_url", "image_urls"} {
		delete(editSource, key)
	}
	editSource["prompt"] = prompt
	editSource["image_references"] = resolved
	editSource["parentPostId"] = parentID
	payload := prepareGrokWebPayload(input, editSource, input.UpstreamModel)
	return c.doGrokJSON(ctx, input, http.MethodPost, base+"/rest/app-chat/conversations/new", payload, c.config.MaxResponseSize)
}

func (c *HTTPClient) uploadEditReferences(ctx context.Context, input Request, base string, references []any) ([]any, *Response, error) {
	results := make([]uploadedReference, len(references))
	assetUserID := credentialUserID(input.Credentials)
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, min(7, len(references)))
	for index, raw := range references {
		index, raw := index, fmt.Sprint(raw)
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				results[index].err = ctx.Err()
				return
			}
			if isHTTPURL(raw) {
				results[index].url = raw
				return
			}
			filename, contentType, encoded, err := parseImageDataURI(raw)
			if err != nil {
				results[index].err = err
				return
			}
			response, err := c.doGrokJSON(ctx, input, http.MethodPost, base+"/rest/app-chat/upload-file", map[string]any{"fileName": filename, "fileMimeType": contentType, "content": encoded}, 2<<20)
			if err != nil {
				results[index].err = err
				return
			}
			if response.StatusCode < 200 || response.StatusCode >= 300 {
				results[index].err = &HTTPError{StatusCode: response.StatusCode, Body: string(response.Body)}
				return
			}
			var payload map[string]any
			if json.Unmarshal(response.Body, &payload) != nil {
				results[index].err = errors.New("decode image upload response")
				return
			}
			fileID := stringValue(payload, "fileMetadataId", "fileId")
			fileURI := stringValue(payload, "fileUri")
			userID := stringValue(payload, "userId", "userID")
			if userID == "" {
				userID = assetUserID
			}
			results[index].url = resolveUploadedReference(fileID, fileURI, userID)
			if results[index].url == "" {
				results[index].err = errors.New("image upload returned no resolvable asset reference")
			}
		}()
	}
	wg.Wait()
	resolved := make([]any, 0, len(results))
	for _, result := range results {
		if result.err != nil {
			var httpErr *HTTPError
			if errors.As(result.err, &httpErr) {
				return nil, &Response{StatusCode: httpErr.StatusCode, Body: json.RawMessage(httpErr.Body)}, nil
			}
			return nil, nil, result.err
		}
		resolved = append(resolved, result.url)
	}
	return resolved, nil, nil
}

func (c *HTTPClient) doGrokJSON(ctx context.Context, input Request, method, endpoint string, payload any, maximum int64) (*Response, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream, application/json")
	copyHeader(request.Header, input.Headers)
	if err := (GrokSSOAdapter{}).Apply(request, input.Credentials); err != nil {
		return nil, err
	}
	client := c.client
	if strings.TrimSpace(input.ProxyURL) != "" {
		client, err = c.clientForProxy(input.ProxyURL)
		if err != nil {
			return nil, fmt.Errorf("configure account proxy: %w", err)
		}
	}
	requestClient := *client
	requestClient.Timeout = c.requestTimeout()
	if input.Operation == OperationVideo && c.config.VideoTimeout > requestClient.Timeout {
		requestClient.Timeout = c.config.VideoTimeout
	}
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
	if strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		result.Events = streamEvents(ctx, response.Body)
		return result, nil
	}
	defer response.Body.Close()
	if maximum <= 0 {
		maximum = 32 << 20
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maximum {
		return nil, errors.New("upstream response exceeds configured limit")
	}
	result.Body = body
	result.Events = Events(parseNonStream(body)...)
	return result, nil
}

func (c *HTTPClient) grokBase(input Request) (string, error) {
	base := strings.TrimSpace(input.Credentials.BaseURL)
	if base == "" {
		base = c.config.GrokBaseURL
	}
	parsed, err := url.Parse(base)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("invalid Grok upstream base URL")
	}
	return strings.TrimRight(base, "/"), nil
}

func parseImageDataURI(value string) (filename, contentType, encoded string, err error) {
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(value)), "data:image/") {
		return "", "", "", errors.New("image edit references must be HTTP URLs or base64 image data URLs")
	}
	header, encoded, ok := strings.Cut(strings.TrimSpace(value)[5:], ",")
	if !ok || !strings.HasSuffix(strings.ToLower(header), ";base64") || encoded == "" {
		return "", "", "", errors.New("invalid image data URL")
	}
	contentType = strings.TrimSpace(strings.TrimSuffix(header, ";base64"))
	if base, _, parseErr := mime.ParseMediaType(contentType); parseErr == nil {
		contentType = base
	}
	if !strings.HasPrefix(strings.ToLower(contentType), "image/") {
		return "", "", "", errors.New("image edit reference has an invalid content type")
	}
	decoded, decodeErr := base64.StdEncoding.DecodeString(encoded)
	if decodeErr != nil || len(decoded) == 0 {
		return "", "", "", errors.New("image edit reference has invalid base64 data")
	}
	if len(decoded) > 25<<20 {
		return "", "", "", errors.New("image edit reference exceeds 25 MiB")
	}
	extensions, _ := mime.ExtensionsByType(contentType)
	extension := ".bin"
	if len(extensions) > 0 {
		extension = extensions[0]
	}
	return "image" + extension, contentType, encoded, nil
}

func resolveUploadedReference(fileID, fileURI, userID string) string {
	if strings.TrimSpace(fileURI) != "" {
		if parsed, err := url.Parse(fileURI); err == nil && parsed.IsAbs() && (parsed.Scheme == "http" || parsed.Scheme == "https") {
			return parsed.String()
		}
		return "https://assets.grok.com/" + strings.TrimLeft(fileURI, "/")
	}
	fileID = strings.TrimSpace(fileID)
	userID = strings.TrimSpace(userID)
	if fileID != "" && userID != "" {
		return "https://assets.grok.com/users/" + url.PathEscape(userID) + "/" + url.PathEscape(fileID) + "/content"
	}
	return ""
}

func isHTTPURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}
