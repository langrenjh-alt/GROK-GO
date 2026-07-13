package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

type grokVideoJob struct {
	ID          string
	Model       string
	Prompt      string
	Seconds     int
	Size        string
	Status      string
	Progress    int
	URL         string
	Thumbnail   string
	Error       string
	ErrorStatus int
	CreatedAt   time.Time
	CompletedAt *time.Time
}

type grokVideoInput struct {
	prompt     string
	seconds    int
	size       string
	ratio      string
	resolution string
	preset     string
	references []any
}

type grokVideoArtifact struct {
	url         string
	thumbnail   string
	videoPostID string
	assetID     string
}

func (c *HTTPClient) startGrokVideo(_ context.Context, input Request) (*Response, error) {
	request, err := parseGrokVideoInput(input.Body)
	if err != nil {
		return nil, err
	}
	job := &grokVideoJob{
		ID: "video_" + strings.ReplaceAll(randomUUID(), "-", ""), Model: input.Model, Prompt: request.prompt,
		Seconds: request.seconds, Size: request.size, Status: "queued", CreatedAt: time.Now().UTC(),
	}
	c.videoMu.Lock()
	c.videoJobs[job.ID] = job
	body, _ := json.Marshal(job.public())
	c.videoMu.Unlock()

	jobContext, cancel := context.WithTimeout(c.config.BackgroundContext, c.config.VideoTimeout)
	go func() {
		defer cancel()
		c.runGrokVideo(jobContext, input, request, job.ID)
		time.AfterFunc(time.Hour, func() {
			c.videoMu.Lock()
			delete(c.videoJobs, job.ID)
			c.videoMu.Unlock()
		})
	}()
	return &Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: body}, nil
}

func (c *HTTPClient) grokVideoStatus(id string) *Response {
	c.videoMu.RLock()
	job := c.videoJobs[strings.TrimSpace(id)]
	var payload map[string]any
	if job != nil {
		payload = job.public()
	}
	c.videoMu.RUnlock()
	if payload == nil {
		return &Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: json.RawMessage(`{"error":{"message":"video job not found"}}`)}
	}
	body, _ := json.Marshal(payload)
	return &Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: body}
}

func (c *HTTPClient) runGrokVideo(ctx context.Context, input Request, request grokVideoInput, jobID string) {
	c.updateVideoJob(jobID, func(job *grokVideoJob) {
		job.Status = "in_progress"
		job.Progress = 1
	})
	base, err := c.grokBase(input)
	if err != nil {
		c.failVideoJob(jobID, 0, err)
		return
	}

	parentID := ""
	resolvedReferences := request.references
	if len(request.references) > 0 {
		resolvedReferences, response, uploadErr := c.uploadEditReferences(ctx, input, base, request.references)
		if uploadErr != nil || response != nil {
			c.failVideoJob(jobID, responseStatus(response), firstError(uploadErr, response))
			return
		}
		postResponse, postErr := c.doGrokJSON(ctx, input, http.MethodPost, base+"/rest/media/post/create", map[string]any{"mediaType": "MEDIA_POST_TYPE_IMAGE", "mediaUrl": resolvedReferences[0]}, 2<<20)
		if postErr != nil || postResponse.StatusCode < 200 || postResponse.StatusCode >= 300 {
			c.failVideoJob(jobID, responseStatus(postResponse), firstError(postErr, postResponse))
			return
		}
		parentID, err = mediaPostID(postResponse.Body)
	} else {
		postResponse, postErr := c.doGrokJSON(ctx, input, http.MethodPost, base+"/rest/media/post/create", map[string]any{"mediaType": "MEDIA_POST_TYPE_VIDEO", "prompt": request.prompt}, 2<<20)
		if postErr != nil || postResponse.StatusCode < 200 || postResponse.StatusCode >= 300 {
			c.failVideoJob(jobID, responseStatus(postResponse), firstError(postErr, postResponse))
			return
		}
		parentID, err = mediaPostID(postResponse.Body)
	}
	if err != nil || parentID == "" {
		c.failVideoJob(jobID, 0, firstError(err, nil))
		return
	}

	segments := videoSegments(request.seconds)
	extendPostID := parentID
	elapsed := 0
	var artifact grokVideoArtifact
	for index, seconds := range segments {
		payload := grokVideoPayload(request, parentID, extendPostID, seconds, elapsed, index > 0, resolvedReferences)
		response, requestErr := c.doGrokJSON(ctx, input, http.MethodPost, base+"/rest/app-chat/conversations/new", payload, c.config.MaxResponseSize)
		if requestErr != nil || response.StatusCode < 200 || response.StatusCode >= 300 {
			c.failVideoJob(jobID, responseStatus(response), firstError(requestErr, response))
			return
		}
		artifact, err = c.collectGrokVideoSegment(ctx, response, jobID, index, len(segments))
		if err != nil {
			c.failVideoJob(jobID, 0, err)
			return
		}
		if artifact.videoPostID != "" {
			extendPostID = artifact.videoPostID
		} else if artifact.assetID != "" {
			extendPostID = artifact.assetID
		}
		elapsed += seconds
	}
	if artifact.url == "" {
		c.failVideoJob(jobID, 0, errors.New("video generation returned no final URL"))
		return
	}
	now := time.Now().UTC()
	c.updateVideoJob(jobID, func(job *grokVideoJob) {
		job.Status = "completed"
		job.Progress = 100
		job.URL = artifact.url
		job.Thumbnail = artifact.thumbnail
		job.CompletedAt = &now
	})
}

func (c *HTTPClient) collectGrokVideoSegment(ctx context.Context, response *Response, jobID string, segment, total int) (grokVideoArtifact, error) {
	if response.Events == nil {
		return grokVideoArtifact{}, errors.New("video upstream did not stream events")
	}
	var artifact grokVideoArtifact
	for event := range response.Events {
		if event.Kind == EventError {
			return artifact, errors.New(event.Error)
		}
		if event.Kind == EventVideo && event.URL != "" {
			artifact.url = event.URL
		}
		if len(event.Raw) > 0 {
			var payload map[string]any
			if json.Unmarshal(event.Raw, &payload) == nil {
				if stream := nestedObject(payload, "result", "response", "streamingVideoGenerationResponse"); stream != nil {
					progress := min(100, max(0, intValue(stream["progress"])))
					scaled := int((float64(segment) + float64(progress)/100) / float64(total) * 100)
					c.updateVideoJob(jobID, func(job *grokVideoJob) { job.Progress = max(job.Progress, scaled) })
					if value := stringValue(stream, "videoUrl"); value != "" {
						artifact.url = absoluteAssetURL(value)
					}
					artifact.thumbnail = absoluteAssetURL(stringValue(stream, "thumbnailImageUrl"))
					artifact.videoPostID = stringValue(stream, "videoPostId", "videoId")
					artifact.assetID = stringValue(stream, "assetId")
				}
			}
		}
		select {
		case <-ctx.Done():
			return artifact, ctx.Err()
		default:
		}
	}
	if artifact.url == "" {
		return artifact, errors.New("video segment returned no final URL")
	}
	return artifact, nil
}

func parseGrokVideoInput(body json.RawMessage) (grokVideoInput, error) {
	var payload map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return grokVideoInput{}, fmt.Errorf("decode video request: %w", err)
	}
	result := grokVideoInput{prompt: strings.TrimSpace(stringValue(payload, "prompt")), seconds: intValue(payload["seconds"]), size: strings.TrimSpace(stringValue(payload, "size")), preset: strings.ToLower(strings.TrimSpace(stringValue(payload, "preset")))}
	if result.prompt == "" {
		return grokVideoInput{}, errors.New("video prompt is required")
	}
	if result.seconds == 0 {
		result.seconds = 6
	}
	if !slices.Contains([]int{6, 10, 12, 16, 20}, result.seconds) {
		return grokVideoInput{}, errors.New("video seconds must be one of 6, 10, 12, 16, or 20")
	}
	if result.size == "" {
		result.size = "720x1280"
	}
	result.ratio, result.resolution = videoSize(result.size)
	if result.ratio == "" {
		return grokVideoInput{}, errors.New("unsupported video size")
	}
	if override := strings.ToLower(strings.TrimSpace(stringValue(payload, "resolution_name"))); override != "" {
		if override != "480p" && override != "720p" {
			return grokVideoInput{}, errors.New("video resolution_name must be 480p or 720p")
		}
		result.resolution = override
	}
	if result.preset == "" {
		result.preset = "custom"
	}
	if !slices.Contains([]string{"fun", "normal", "spicy", "custom"}, result.preset) {
		return grokVideoInput{}, errors.New("video preset must be fun, normal, spicy, or custom")
	}
	result.references = videoReferences(payload)
	return result, nil
}

func grokVideoPayload(request grokVideoInput, parentID, extendID string, seconds, elapsed int, extend bool, references []any) map[string]any {
	config := map[string]any{"parentPostId": parentID, "aspectRatio": request.ratio, "videoLength": seconds, "resolutionName": request.resolution}
	if extend {
		config["isVideoExtension"] = true
		config["videoExtensionStartTime"] = float64(elapsed) + 1.0/24.0
		config["extendPostId"] = extendID
		config["stitchWithExtendPostId"] = true
		config["originalPrompt"] = request.prompt
		config["originalPostId"] = parentID
		config["originalRefType"] = "ORIGINAL_REF_TYPE_VIDEO_EXTENSION"
		config["mode"] = request.preset
		config["isVideoEdit"] = false
	} else if len(references) > 0 {
		config["isVideoEdit"] = false
		config["isReferenceToVideo"] = true
		config["imageReferences"] = references
	}
	return map[string]any{
		"temporary": true, "modelName": "imagine-video-gen", "message": videoPrompt(request.prompt, request.preset), "enableSideBySide": true,
		"responseMetadata": map[string]any{"experiments": []any{}, "modelConfigOverride": map[string]any{"modelMap": map[string]any{"videoGenModelConfig": config}}},
	}
}

func (j *grokVideoJob) public() map[string]any {
	result := map[string]any{
		"id": j.ID, "object": "video", "created_at": j.CreatedAt.Unix(), "status": j.Status,
		"model": j.Model, "progress": j.Progress, "prompt": j.Prompt, "seconds": strconv.Itoa(j.Seconds), "size": j.Size, "quality": "standard",
	}
	if j.CompletedAt != nil {
		result["completed_at"] = j.CompletedAt.Unix()
	}
	if j.URL != "" {
		result["url"] = j.URL
	}
	if j.Thumbnail != "" {
		result["thumbnail_url"] = j.Thumbnail
	}
	if j.Error != "" {
		result["error"] = map[string]any{"code": "video_generation_failed", "message": j.Error, "upstream_status": j.ErrorStatus}
	}
	return result
}

func (c *HTTPClient) updateVideoJob(id string, update func(*grokVideoJob)) {
	c.videoMu.Lock()
	defer c.videoMu.Unlock()
	if job := c.videoJobs[id]; job != nil {
		update(job)
	}
}

func (c *HTTPClient) failVideoJob(id string, status int, err error) {
	message := "video generation failed"
	if err != nil {
		message = err.Error()
	}
	now := time.Now().UTC()
	c.updateVideoJob(id, func(job *grokVideoJob) {
		job.Status = "failed"
		job.Error = message
		job.ErrorStatus = status
		job.CompletedAt = &now
	})
}

func mediaPostID(body []byte) (string, error) {
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil {
		return "", errors.New("decode media post response")
	}
	post, _ := payload["post"].(map[string]any)
	id := stringValue(post, "id")
	if id == "" {
		return "", errors.New("media post returned no id")
	}
	return id, nil
}

func nestedObject(payload map[string]any, keys ...string) map[string]any {
	var current any = payload
	for _, key := range keys {
		mapping, _ := current.(map[string]any)
		if mapping == nil {
			return nil
		}
		current = mapping[key]
	}
	result, _ := current.(map[string]any)
	return result
}

func videoSegments(seconds int) []int {
	switch seconds {
	case 6, 10:
		return []int{seconds}
	case 12:
		return []int{6, 6}
	case 16:
		return []int{10, 6}
	case 20:
		return []int{10, 10}
	default:
		return nil
	}
}

func videoSize(size string) (string, string) {
	switch size {
	case "720x1280", "1024x1792":
		return "9:16", "720p"
	case "1280x720", "1792x1024":
		return "16:9", "720p"
	case "1024x1024":
		return "1:1", "720p"
	default:
		return "", ""
	}
}

func videoPrompt(prompt, preset string) string {
	flags := map[string]string{"fun": "--mode=extremely-crazy", "normal": "--mode=normal", "spicy": "--mode=extremely-spicy-or-crazy", "custom": "--mode=custom"}
	return strings.TrimSpace(prompt + " " + flags[preset])
}

func videoReferences(payload map[string]any) []any {
	for _, key := range []string{"input_reference[]", "input_reference", "input_references", "image_references"} {
		if _, ok := payload[key]; !ok {
			continue
		}
		copy := map[string]any{"image_references": payload[key]}
		return imageReferences(copy)
	}
	return nil
}

func absoluteAssetURL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
		return value
	}
	return "https://assets.grok.com/" + strings.TrimLeft(value, "/")
}

func responseStatus(response *Response) int {
	if response == nil {
		return 0
	}
	return response.StatusCode
}

func firstError(err error, response *Response) error {
	if err != nil {
		return err
	}
	if response != nil {
		return &HTTPError{StatusCode: response.StatusCode, Body: string(response.Body)}
	}
	return errors.New("upstream request failed")
}
