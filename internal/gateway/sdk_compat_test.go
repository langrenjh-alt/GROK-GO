package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/langrenjh-alt/GROK-GO/internal/upstream"
)

func TestOfficialJavaScriptSDKCompatibility(t *testing.T) {
	if testing.Short() {
		t.Skip("SDK compatibility test is an integration test")
	}
	pnpm := "pnpm"
	if runtime.GOOS == "windows" {
		pnpm = "pnpm.cmd"
	}
	if _, err := exec.LookPath(pnpm); err != nil {
		t.Skip("pnpm is not installed")
	}
	client := upstream.ClientFunc(func(_ context.Context, request upstream.Request) (*upstream.Response, error) {
		if request.Operation == upstream.OperationImage {
			return &upstream.Response{StatusCode: http.StatusOK, Body: []byte(`{"created":1,"data":[{"url":"https://cdn.test/image.png"}]}`)}, nil
		}
		return &upstream.Response{StatusCode: http.StatusOK, Events: upstream.Events(
			upstream.Event{Kind: upstream.EventTextDelta, Text: "sdk-compatible"},
			upstream.Event{Kind: upstream.EventUsage, Usage: upstream.Usage{InputTokens: 2, OutputTokens: 1}},
			upstream.Event{Kind: upstream.EventDone},
		)}, nil
	})
	server := httptest.NewServer(testGateway(t, client))
	defer server.Close()

	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(pnpm, "--dir", "web", "exec", "node", "tests/sdk-compat.mjs", server.URL)
	command.Dir = repositoryRoot
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("SDK compatibility test failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), `"openai":true`) || !strings.Contains(string(output), `"anthropic":true`) {
		t.Fatalf("unexpected SDK compatibility output: %s", output)
	}
}
