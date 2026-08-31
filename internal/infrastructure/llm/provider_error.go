package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	openai "github.com/meguminnnnnnnnn/go-openai"
)

type ModelResponseError struct{}

func (e *ModelResponseError) Error() string {
	return "model returned an empty response"
}

func (e *ModelResponseError) SafeDiagnosticCode() string {
	return "empty_model_response"
}

type ProviderError struct {
	Operation  string
	StatusCode int
	Code       string
	Retryable  bool
	Cause      error
}

func (e *ProviderError) Error() string {
	if e.StatusCode > 0 {
		return fmt.Sprintf("%s provider request failed with status %d", e.Operation, e.StatusCode)
	}
	return fmt.Sprintf("%s provider request failed", e.Operation)
}

func (e *ProviderError) Unwrap() error { return e.Cause }

func normalizeProviderError(operation string, ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if err == context.DeadlineExceeded {
		return err
	}
	status, code := 0, ""
	var einoErr *einoopenai.APIError
	if errors.As(err, &einoErr) {
		status, code = einoErr.HTTPStatusCode, fmt.Sprint(einoErr.Code)
	}
	var apiErr *openai.APIError
	if status == 0 && errors.As(err, &apiErr) {
		status, code = apiErr.HTTPStatusCode, fmt.Sprint(apiErr.Code)
	}
	var requestErr *openai.RequestError
	if status == 0 && errors.As(err, &requestErr) {
		status = requestErr.HTTPStatusCode
	}
	retryable := status == 408 || status == 429 || status == 500 || status == 502 || status == 503 || status == 504
	var netErr net.Error
	if status == 0 && errors.As(err, &netErr) && (netErr.Timeout() || netErr.Temporary()) {
		retryable = true
	}
	if status == 0 && errors.Is(err, io.ErrUnexpectedEOF) {
		retryable = true
	}
	return &ProviderError{Operation: operation, StatusCode: status, Code: code, Retryable: retryable, Cause: err}
}

type retryPolicy struct {
	maxAttempts int
	backoffs    []time.Duration
	wait        func(context.Context, time.Duration) error
}

func defaultRetryPolicy() retryPolicy {
	return retryPolicy{
		maxAttempts: 5,
		backoffs: []time.Duration{
			2 * time.Second,
			5 * time.Second,
			15 * time.Second,
			30 * time.Second,
		},
		wait: waitForRetry,
	}
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func isRetryableModelError(err error) bool {
	var providerErr *ProviderError
	if errors.As(err, &providerErr) {
		return providerErr.Retryable
	}
	var responseErr *ModelResponseError
	return errors.As(err, &responseErr)
}

func withRetry[T any](
	ctx context.Context,
	policy retryPolicy,
	operation func() (T, error),
) (T, error) {
	return withRetryIf(ctx, policy, operation, nil)
}

func withRetryIf[T any](
	ctx context.Context,
	policy retryPolicy,
	operation func() (T, error),
	allowRetry func(T, error) bool,
) (T, error) {
	var zero T
	if policy.maxAttempts < 1 {
		policy.maxAttempts = 1
	}
	if policy.wait == nil {
		policy.wait = waitForRetry
	}
	for attempt := 0; attempt < policy.maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		value, err := operation()
		if err == nil {
			return value, nil
		}
		if !isRetryableModelError(err) ||
			(allowRetry != nil && !allowRetry(value, err)) ||
			attempt+1 >= policy.maxAttempts {
			return zero, err
		}
		delay := time.Duration(0)
		if len(policy.backoffs) > 0 {
			delay = policy.backoffs[min(attempt, len(policy.backoffs)-1)]
		}
		if err := policy.wait(ctx, delay); err != nil {
			return zero, err
		}
	}
	return zero, errors.New("retry attempts exhausted")
}
