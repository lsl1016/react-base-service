package llm

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseRetryAfterSeconds(t *testing.T) {
	if got := ParseRetryAfter("3"); got != 3*time.Second {
		t.Fatalf("expected 3s, got %v", got)
	}
}

func TestParseRetryAfterHTTPDate(t *testing.T) {
	future := time.Now().Add(30 * time.Second).UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT")
	got := ParseRetryAfter(future)
	if got <= 0 || got > time.Minute {
		t.Fatalf("expected positive delay near 30s, got %v", got)
	}
}

func TestParseRetryAfterInvalidOrExcessive(t *testing.T) {
	for _, header := range []string{"", "  ", "abc", "-5", "3600"} {
		if got := ParseRetryAfter(header); got != 0 {
			t.Fatalf("header %q should parse to 0, got %v", header, got)
		}
	}
}

func TestAPIErrorMessageTruncated(t *testing.T) {
	err := newAPIError("claude", 429, time.Second, strings.Repeat("x", 5000))
	if len(err.Message) != maxAPIErrorMessageLimit {
		t.Fatalf("message must be truncated to %d, got %d", maxAPIErrorMessageLimit, len(err.Message))
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 429 {
		t.Fatalf("APIError must survive errors.As: %v", err)
	}
	if !strings.Contains(err.Error(), "retry-after 1s") {
		t.Fatalf("error text should include retry-after: %s", err.Error())
	}
}
