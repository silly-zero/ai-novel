package llm

import (
	"context"
	"errors"
	"testing"
	"time"

	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
)

func TestNormalizeProviderErrorClassifiesHTTPStatus(t *testing.T) {
	cause := &einoopenai.APIError{HTTPStatusCode: 429, Code: "rate_limit"}
	err := normalizeProviderError("chat", context.Background(), cause)
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || providerErr.StatusCode != 429 || !providerErr.Retryable || !errors.Is(err, cause) {
		t.Fatalf("error = %#v", err)
	}
	if providerErr.Error() == cause.Error() || providerErr.Error() == "" {
		t.Fatalf("unsafe provider error = %q", providerErr.Error())
	}
}

func TestNormalizeProviderErrorDoesNotRetryAuth(t *testing.T) {
	err := normalizeProviderError("chat", context.Background(), &einoopenai.APIError{HTTPStatusCode: 401})
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || providerErr.Retryable {
		t.Fatalf("error = %#v", err)
	}
}

func TestWithRetrySucceedsAfterTransientErrors(t *testing.T) {
	calls := 0
	waits := 0
	policy := retryPolicy{maxAttempts: 3, backoffs: []time.Duration{time.Millisecond}, wait: func(context.Context, time.Duration) error { waits++; return nil }}
	value, err := withRetry(context.Background(), policy, func() (string, error) {
		calls++
		if calls < 3 {
			return "", &ProviderError{Operation: "chat", StatusCode: 429, Retryable: true}
		}
		return "ok", nil
	})
	if err != nil || value != "ok" || calls != 3 || waits != 2 {
		t.Fatalf("value=%q err=%v calls=%d waits=%d", value, err, calls, waits)
	}
}

func TestWithRetryStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	policy := retryPolicy{maxAttempts: 3, backoffs: []time.Duration{time.Millisecond}, wait: func(context.Context, time.Duration) error { cancel(); return context.Canceled }}
	_, err := withRetry(ctx, policy, func() (string, error) {
		calls++
		return "", &ProviderError{Operation: "chat", StatusCode: 429, Retryable: true}
	})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}
