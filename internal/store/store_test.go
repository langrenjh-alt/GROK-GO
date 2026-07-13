package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPaginationNormalized(t *testing.T) {
	if got := (Pagination{}).normalized(); got.Limit != 50 || got.Offset != 0 {
		t.Fatalf("normalized default = %+v", got)
	}
	if got := (Pagination{Limit: 900, Offset: -1}).normalized(); got.Limit != 500 || got.Offset != 0 {
		t.Fatalf("normalized limits = %+v", got)
	}
}

func TestListAndCountFiltersShareSearchSemantics(t *testing.T) {
	accountWhere, accountArgs := accountFilterSQL(AccountFilter{Query: "team"})
	if len(accountArgs) != 1 || !strings.Contains(accountWhere, "tags::text ILIKE") {
		t.Fatalf("account filter = %q, %#v", accountWhere, accountArgs)
	}
	modelWhere, modelArgs := modelFilterSQL(ModelFilter{Query: "vision"})
	if len(modelArgs) != 1 || !strings.Contains(modelWhere, "aliases::text ILIKE") {
		t.Fatalf("model filter = %q, %#v", modelWhere, modelArgs)
	}
	requestWhere, requestArgs := requestLogFilterSQL(RequestLogFilter{Query: "gateway", StatusMin: 500, StatusMax: 599})
	if len(requestArgs) != 3 || !strings.Contains(requestWhere, "error_summary ILIKE") || !strings.Contains(requestWhere, "status_code >=") || !strings.Contains(requestWhere, "status_code <=") {
		t.Fatalf("request log filter = %q, %#v", requestWhere, requestArgs)
	}
	auditWhere, auditArgs := auditLogFilterSQL(AuditLogFilter{Query: "203.0.113.10"})
	if len(auditArgs) != 1 || !strings.Contains(auditWhere, "ip_address ILIKE") {
		t.Fatalf("audit log filter = %q, %#v", auditWhere, auditArgs)
	}
}

func TestUsageBucketStartsUseUTCBoundaries(t *testing.T) {
	location := time.FixedZone("UTC+8", 8*60*60)
	createdAt := time.Date(2026, time.August, 1, 7, 8, 9, 0, location)
	minute, day, month := usageBucketStarts(createdAt)
	if minute != time.Date(2026, time.July, 31, 23, 8, 0, 0, time.UTC) {
		t.Fatalf("minute start = %s", minute)
	}
	if day != time.Date(2026, time.July, 31, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("day start = %s", day)
	}
	if month != time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("month start = %s", month)
	}
}

func TestCacheHitRateUsesOnlyValidInputTokens(t *testing.T) {
	tests := []struct {
		name   string
		cached int64
		input  int64
		want   float64
	}{
		{name: "partial hit", cached: 40, input: 100, want: 40},
		{name: "missing usage", cached: 50, input: 0, want: 0},
		{name: "negative cache", cached: -10, input: 100, want: 0},
		{name: "malformed over-report", cached: 150, input: 100, want: 100},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := CacheHitRate(test.cached, test.input); got != test.want {
				t.Fatalf("CacheHitRate(%d, %d) = %v, want %v", test.cached, test.input, got, test.want)
			}
		})
	}
}

func TestMarshalJSONReturnsJSONText(t *testing.T) {
	value, err := marshalJSON([]string{"grok-4.5"}, "[]")
	if err != nil || value != `["grok-4.5"]` {
		t.Fatalf("marshalJSON() = %q, %v", value, err)
	}
	value, err = marshalJSON(nil, "[]")
	if err != nil || value != "[]" {
		t.Fatalf("marshalJSON(nil) = %q, %v", value, err)
	}
}

func TestTranslateNotFound(t *testing.T) {
	if err := translateError(pgx.ErrNoRows); !errors.Is(err, ErrNotFound) {
		t.Fatalf("translateError() = %v", err)
	}
}
