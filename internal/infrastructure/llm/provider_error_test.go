package llm

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
)

func TestNormalizeProviderErrorClassifiesHTTPStatus(t *testing.T) {
	tests := []struct {
		status    int
		retryable bool
	}{
		{408, true},
		{429, true},
		{500, true},
		{502, true},
		{503, true},
		{504, true},
		{400, false},
		{401, false},
		{403, false},
		{501, false},
		{599, false},
	}
	for _, test := range tests {
		t.Run(fmt.Sprintf("status_%d", test.status), func(t *testing.T) {
			cause := &einoopenai.APIError{
				HTTPStatusCode: test.status,
				Code:           "CANARY_PROVIDER_CODE",
				Message:        "CANARY_PROVIDER_MESSAGE",
			}
			err := normalizeProviderError("chat", context.Background(), cause)
			var providerErr *ProviderError
			if !errors.As(err, &providerErr) ||
				providerErr.StatusCode != test.status ||
				providerErr.Retryable != test.retryable ||
				!errors.Is(err, cause) {
				t.Fatalf("error = %#v", err)
			}
			for _, secret := range []string{"CANARY_PROVIDER_CODE", "CANARY_PROVIDER_MESSAGE"} {
				if strings.Contains(providerErr.Error(), secret) {
					t.Fatalf("provider error leaked %q: %s", secret, providerErr.Error())
				}
			}
		})
	}
}

func TestNormalizeProviderErrorDoesNotRetryAuth(t *testing.T) {
	err := normalizeProviderError("chat", context.Background(), &einoopenai.APIError{HTTPStatusCode: 401})
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || providerErr.Retryable {
		t.Fatalf("error = %#v", err)
	}
}

func TestDefaultRetryPolicyUsesBoundedExponentialBackoff(t *testing.T) {
	policy := defaultRetryPolicy()
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}
	if policy.maxAttempts != 5 || !slices.Equal(policy.backoffs, want) {
		t.Fatalf("policy = %#v, want attempts=5 backoffs=%v", policy, want)
	}
}

func TestWithRetryUsesConfiguredBackoffSequence(t *testing.T) {
	calls := 0
	var waits []time.Duration
	finalErr := &ProviderError{Operation: "chat", StatusCode: 429, Retryable: true}
	policy := defaultRetryPolicy()
	policy.wait = func(_ context.Context, delay time.Duration) error {
		waits = append(waits, delay)
		return nil
	}

	_, err := withRetry(context.Background(), policy, func() (string, error) {
		calls++
		return "", finalErr
	})

	if !errors.Is(err, finalErr) || calls != 5 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}
	if !slices.Equal(waits, want) {
		t.Fatalf("waits=%v, want=%v", waits, want)
	}
}

func TestWithRetryDoesNotRetryPermanentProviderError(t *testing.T) {
	calls := 0
	_, err := withRetry(context.Background(), defaultRetryPolicy(), func() (string, error) {
		calls++
		return "", &ProviderError{Operation: "chat", StatusCode: 401}
	})
	if err == nil || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestNormalizeProviderErrorPreservesWrappedCancellation(t *testing.T) {
	err := normalizeProviderError(
		"chat",
		context.Background(),
		fmt.Errorf("transport stopped: %w", context.Canceled),
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
	var providerErr *ProviderError
	if errors.As(err, &providerErr) {
		t.Fatalf("wrapped cancellation classified as provider error: %#v", providerErr)
	}
}

func TestNormalizeProviderErrorPrefersContextOutcome(t *testing.T) {
	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		assertContextProviderError(t, ctx, context.Canceled)
	})
	t.Run("deadline", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		assertContextProviderError(t, ctx, context.DeadlineExceeded)
	})
}

func assertContextProviderError(t *testing.T, ctx context.Context, want error) {
	t.Helper()
	err := normalizeProviderError(
		"chat",
		ctx,
		&einoopenai.APIError{HTTPStatusCode: 429, Message: "CANARY_PROVIDER_MESSAGE"},
	)
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	var providerErr *ProviderError
	if errors.As(err, &providerErr) {
		t.Fatalf("context outcome wrapped as provider error: %#v", providerErr)
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
