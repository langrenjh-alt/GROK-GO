package api

import (
	"net/http"
	"strings"
	"testing"
)

func TestListEnvelopeKeepsServerTotalAcrossPages(t *testing.T) {
	envelope := listEnvelope([]string{"third"}, 1, 2, 7)
	if envelope["total"] != int64(7) || envelope["page"] != 3 || envelope["page_size"] != 1 {
		t.Fatalf("pagination envelope = %#v", envelope)
	}
}

func TestRequestLogStatusClassRejectsUnknownRanges(t *testing.T) {
	environment := newAuthenticatedEnvironment(t)
	response := environment.request(t, http.MethodGet, "/logs?status_class=7xx", nil, environment.cookie, "")
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_status_class") {
		t.Fatalf("status class response = %d %s", response.Code, response.Body.String())
	}
}
