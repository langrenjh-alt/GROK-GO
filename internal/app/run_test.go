package app

import (
	"testing"
	"time"
)

func TestBackgroundOAuthRefreshSchedule(t *testing.T) {
	if backgroundOAuthRefreshInterval != 15*time.Minute || backgroundOAuthRefreshBefore != time.Hour {
		t.Fatalf("OAuth refresh schedule = interval %s, before %s", backgroundOAuthRefreshInterval, backgroundOAuthRefreshBefore)
	}
}

func TestRuntimeInstanceID(t *testing.T) {
	if value := runtimeInstanceID("node-a", ":8080"); value != "node-a" {
		t.Fatalf("explicit instance ID = %q", value)
	}
	first := runtimeInstanceID("", "127.0.0.1:8080")
	second := runtimeInstanceID("", "127.0.0.1:8080")
	other := runtimeInstanceID("", "127.0.0.1:8081")
	if first == "" || first != second || first == other {
		t.Fatalf("derived instance IDs = %q, %q, %q", first, second, other)
	}
}
