package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type generateTestResult struct {
	message *schema.Message
	err     error
}

type streamTestResult struct {
	stream *schema.StreamReader[*schema.Message]
	err    error
}

type adapterTestChatModel struct {
	generateResults []generateTestResult
	generateCalls   int
	stream          *schema.StreamReader[*schema.Message]
	err             error
	results         []streamTestResult
	calls           int
}

func (f *adapterTestChatModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	f.generateCalls++
	if len(f.generateResults) == 0 {
		return nil, nil
	}
	result := f.generateResults[0]
	f.generateResults = f.generateResults[1:]
	return result.message, result.err
}

func (f *adapterTestChatModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	f.calls++
	if len(f.results) > 0 {
		result := f.results[0]
		f.results = f.results[1:]
		return result.stream, result.err
	}
	return f.stream, f.err
}

func (f *adapterTestChatModel) BindTools([]*schema.ToolInfo) error {
	return nil
}

func TestNewOpenAIAdapterAppliesModelAndMaxTokens(t *testing.T) {
	requests := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("request path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer chat-test-key" {
			t.Error("request did not use chat API key")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		requests <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()

	adapter, err := NewOpenAIAdapter(context.Background(), ChatConfig{
		APIKey:    "chat-test-key",
		BaseURL:   server.URL,
		Model:     "chat-test-model",
		MaxTokens: 1234,
		Timeout:   time.Second,
	})
	if err != nil {
		t.Fatalf("NewOpenAIAdapter returned error: %v", err)
	}
	if _, err := adapter.Generate(context.Background(), "system", "user"); err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	body := <-requests
	if body["model"] != "chat-test-model" || body["max_tokens"] != float64(1234) {
		t.Fatalf("request body = %#v", body)
	}
}

func TestNewOpenAIAdapterAppliesOptionalTemperature(t *testing.T) {
	for _, test := range []struct {
		name        string
		temperature *float32
		wantField   bool
	}{
		{name: "unset"},
		{name: "explicit zero", temperature: float32Pointer(0), wantField: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			requests := make(chan map[string]any, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				requests <- body
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
			}))
			defer server.Close()

			adapter, err := NewOpenAIAdapter(context.Background(), ChatConfig{
				APIKey:      "chat-test-key",
				BaseURL:     server.URL,
				Model:       "chat-test-model",
				MaxTokens:   100,
				Temperature: test.temperature,
				Timeout:     time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := adapter.GenerateJSONObject(context.Background(), "system", "user"); err != nil {
				t.Fatal(err)
			}
			body := <-requests
			value, exists := body["temperature"]
			if exists != test.wantField || exists && value != float64(0) {
				t.Fatalf("request body = %#v", body)
			}
		})
	}
}

func float32Pointer(value float32) *float32 {
	return &value
}

func TestOpenAIAdapterJSONObjectModeIsPerCall(t *testing.T) {
	requests := make(chan map[string]any, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		requests <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{\"ok\":true}"}}]}`))
	}))
	defer server.Close()

	adapter, err := NewOpenAIAdapter(context.Background(), ChatConfig{
		APIKey:    "chat-test-key",
		BaseURL:   server.URL,
		Model:     "chat-test-model",
		MaxTokens: 100,
		Timeout:   time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.GenerateJSONObject(context.Background(), "system", "return JSON"); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Generate(context.Background(), "system", "return text"); err != nil {
		t.Fatal(err)
	}

	jsonBody := <-requests
	format, ok := jsonBody["response_format"].(map[string]any)
	if !ok || format["type"] != "json_object" {
		t.Fatalf("JSON request body = %#v", jsonBody)
	}
	plainBody := <-requests
	if _, exists := plainBody["response_format"]; exists {
		t.Fatalf("plain request inherited response_format: %#v", plainBody)
	}
}

func TestOpenAIAdapterJSONObjectModeSurvivesRetry(t *testing.T) {
	var mu sync.Mutex
	var requests []map[string]any
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		mu.Lock()
		requests = append(requests, body)
		calls++
		current := calls
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if current == 1 {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":""}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{\"ok\":true}"}}]}`))
	}))
	defer server.Close()

	adapter, err := NewOpenAIAdapter(context.Background(), ChatConfig{
		APIKey:    "chat-test-key",
		BaseURL:   server.URL,
		Model:     "chat-test-model",
		MaxTokens: 100,
		Timeout:   time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter.retryPolicy = noWaitRetryPolicy(2)
	if _, err := adapter.GenerateJSONObject(context.Background(), "system", "return JSON"); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("requests=%d", len(requests))
	}
	for index, body := range requests {
		format, ok := body["response_format"].(map[string]any)
		if !ok || format["type"] != "json_object" {
			t.Fatalf("request %d body=%#v", index, body)
		}
	}
}

func TestNewOpenAIAdapterAppliesTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer server.Close()

	adapter, err := NewOpenAIAdapter(context.Background(), ChatConfig{
		APIKey:    "chat-test-key",
		BaseURL:   server.URL,
		Model:     "chat-test-model",
		MaxTokens: 100,
		Timeout:   10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewOpenAIAdapter returned error: %v", err)
	}
	adapter.retryPolicy = retryPolicy{maxAttempts: 1}
	_, err = adapter.Generate(context.Background(), "system", "user")
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || !providerErr.Retryable {
		t.Fatalf("Generate error = %v, want retryable provider timeout", err)
	}
}

func TestOpenAIAdapterStreamGenerateCompletesOnEOF(t *testing.T) {
	reader := schema.StreamReaderFromArray([]*schema.Message{
		schema.AssistantMessage("第一段", nil),
		schema.AssistantMessage("第二段", nil),
	})
	adapter := &OpenAIAdapter{chatModel: &adapterTestChatModel{stream: reader}}

	var content string
	err := adapter.StreamGenerate(context.Background(), "system", "user", func(chunk string) error {
		content += chunk
		return nil
	})
	if err != nil {
		t.Fatalf("StreamGenerate() error = %v", err)
	}
	if content != "第一段第二段" {
		t.Fatalf("content = %q, want %q", content, "第一段第二段")
	}
}

func TestOpenAIAdapterStreamGenerateReturnsReceiveError(t *testing.T) {
	receiveErr := errors.New("provider stream failed")
	reader, writer := schema.Pipe[*schema.Message](1)
	go func() {
		writer.Send(schema.AssistantMessage("部分正文", nil), nil)
		writer.Send(nil, receiveErr)
		writer.Close()
	}()
	adapter := &OpenAIAdapter{chatModel: &adapterTestChatModel{stream: reader}}

	err := adapter.StreamGenerate(context.Background(), "system", "user", func(string) error { return nil })
	if !errors.Is(err, receiveErr) {
		t.Fatalf("StreamGenerate() error = %v, want wrapped %v", err, receiveErr)
	}
}

func TestOpenAIAdapterStreamGenerateReturnsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reader, writer := schema.Pipe[*schema.Message](0)
	go func() {
		writer.Send(nil, errors.New("provider canceled"))
		writer.Close()
	}()
	adapter := &OpenAIAdapter{chatModel: &adapterTestChatModel{stream: reader}}

	err := adapter.StreamGenerate(ctx, "system", "user", func(string) error { return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("StreamGenerate() error = %v, want context canceled", err)
	}
}

func TestOpenAIAdapterStreamGenerateReturnsContextCancellationOnEOF(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reader := schema.StreamReaderFromArray([]*schema.Message{
		schema.AssistantMessage("部分正文", nil),
	})
	adapter := &OpenAIAdapter{chatModel: &adapterTestChatModel{stream: reader}}

	err := adapter.StreamGenerate(ctx, "system", "user", func(string) error {
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("StreamGenerate() error = %v, want context canceled", err)
	}
}

func TestOpenAIAdapterStreamGenerateReturnsCallbackError(t *testing.T) {
	callbackErr := errors.New("consumer stopped")
	reader := schema.StreamReaderFromArray([]*schema.Message{
		schema.AssistantMessage("第一段", nil),
		schema.AssistantMessage("第二段", nil),
	})
	adapter := &OpenAIAdapter{chatModel: &adapterTestChatModel{stream: reader}}

	calls := 0
	err := adapter.StreamGenerate(context.Background(), "system", "user", func(string) error {
		calls++
		return callbackErr
	})
	if !errors.Is(err, callbackErr) {
		t.Fatalf("StreamGenerate() error = %v, want callback error", err)
	}
	if calls != 1 {
		t.Fatalf("callback calls = %d, want 1", calls)
	}
}

func noWaitRetryPolicy(maxAttempts int) retryPolicy {
	return retryPolicy{
		maxAttempts: maxAttempts,
		backoffs:    []time.Duration{time.Millisecond},
		wait:        func(context.Context, time.Duration) error { return nil },
	}
}

func TestOpenAIAdapterGenerateRetriesEmptyResponses(t *testing.T) {
	model := &adapterTestChatModel{generateResults: []generateTestResult{
		{},
		{message: schema.AssistantMessage("   ", nil)},
		{message: schema.AssistantMessage("有效响应", nil)},
	}}
	adapter := &OpenAIAdapter{chatModel: model, retryPolicy: noWaitRetryPolicy(5)}

	got, err := adapter.Generate(context.Background(), "system", "user")

	if err != nil || got != "有效响应" || model.generateCalls != 3 {
		t.Fatalf("got=%q err=%v calls=%d", got, err, model.generateCalls)
	}
}

func TestOpenAIAdapterGenerateReturnsTypedErrorAfterEmptyResponses(t *testing.T) {
	model := &adapterTestChatModel{}
	adapter := &OpenAIAdapter{chatModel: model, retryPolicy: noWaitRetryPolicy(3)}

	_, err := adapter.Generate(context.Background(), "system", "user")

	var responseErr *ModelResponseError
	var providerErr *ProviderError
	if !errors.As(err, &responseErr) || errors.As(err, &providerErr) ||
		responseErr.SafeDiagnosticCode() != "empty_model_response" ||
		model.generateCalls != 3 {
		t.Fatalf("err=%#v calls=%d", err, model.generateCalls)
	}
}

func TestOpenAIAdapterStreamGenerateRetriesEmptyEOF(t *testing.T) {
	model := &adapterTestChatModel{results: []streamTestResult{
		{stream: schema.StreamReaderFromArray([]*schema.Message{})},
		{stream: schema.StreamReaderFromArray([]*schema.Message{
			schema.AssistantMessage("正文", nil),
		})},
	}}
	adapter := &OpenAIAdapter{chatModel: model, retryPolicy: noWaitRetryPolicy(3)}
	var chunks []string

	err := adapter.StreamGenerate(context.Background(), "system", "user", func(chunk string) error {
		chunks = append(chunks, chunk)
		return nil
	})

	if err != nil || model.calls != 2 || !slices.Equal(chunks, []string{"正文"}) {
		t.Fatalf("err=%v calls=%d chunks=%v", err, model.calls, chunks)
	}
}

func TestOpenAIAdapterStreamGenerateRetriesWhitespaceOnlyEOF(t *testing.T) {
	model := &adapterTestChatModel{results: []streamTestResult{
		{stream: schema.StreamReaderFromArray([]*schema.Message{
			schema.AssistantMessage("  \n", nil),
		})},
		{stream: schema.StreamReaderFromArray([]*schema.Message{
			schema.AssistantMessage("正文", nil),
		})},
	}}
	adapter := &OpenAIAdapter{chatModel: model, retryPolicy: noWaitRetryPolicy(3)}
	var chunks []string

	err := adapter.StreamGenerate(context.Background(), "system", "user", func(chunk string) error {
		chunks = append(chunks, chunk)
		return nil
	})

	if err != nil || model.calls != 2 || !slices.Equal(chunks, []string{"正文"}) {
		t.Fatalf("err=%v calls=%d chunks=%q", err, model.calls, chunks)
	}
}

func TestOpenAIAdapterStreamGenerateRetriesNilStream(t *testing.T) {
	model := &adapterTestChatModel{results: []streamTestResult{
		{},
		{stream: schema.StreamReaderFromArray([]*schema.Message{
			schema.AssistantMessage("正文", nil),
		})},
	}}
	adapter := &OpenAIAdapter{chatModel: model, retryPolicy: noWaitRetryPolicy(3)}
	var content string

	err := adapter.StreamGenerate(context.Background(), "system", "user", func(chunk string) error {
		content += chunk
		return nil
	})

	if err != nil || model.calls != 2 || content != "正文" {
		t.Fatalf("err=%v calls=%d content=%q", err, model.calls, content)
	}
}

func TestOpenAIAdapterStreamGenerateExhaustsEmptyResponses(t *testing.T) {
	model := &adapterTestChatModel{}
	adapter := &OpenAIAdapter{chatModel: model, retryPolicy: noWaitRetryPolicy(3)}

	err := adapter.StreamGenerate(context.Background(), "system", "user", func(string) error { return nil })

	var responseErr *ModelResponseError
	var providerErr *ProviderError
	if !errors.As(err, &responseErr) || errors.As(err, &providerErr) || model.calls != 3 {
		t.Fatalf("err=%#v calls=%d", err, model.calls)
	}
}

func TestOpenAIAdapterStreamGenerateRetriesStartupBeforeContent(t *testing.T) {
	model := &adapterTestChatModel{results: []streamTestResult{
		{err: &einoopenai.APIError{
			HTTPStatusCode: 429,
			Message:        "CANARY_PROVIDER_MESSAGE",
		}},
		{stream: schema.StreamReaderFromArray([]*schema.Message{
			schema.AssistantMessage("正文", nil),
		})},
	}}
	adapter := &OpenAIAdapter{chatModel: model, retryPolicy: noWaitRetryPolicy(3)}
	var content string

	err := adapter.StreamGenerate(context.Background(), "system", "user", func(chunk string) error {
		content += chunk
		return nil
	})

	if err != nil || model.calls != 2 || content != "正文" {
		t.Fatalf("err=%v calls=%d content=%q", err, model.calls, content)
	}
}

func TestOpenAIAdapterStreamGenerateDoesNotRetryPermanentStartupError(t *testing.T) {
	cause := &einoopenai.APIError{HTTPStatusCode: 401, Message: "CANARY_PROVIDER_MESSAGE"}
	model := &adapterTestChatModel{err: cause}
	adapter := &OpenAIAdapter{chatModel: model, retryPolicy: noWaitRetryPolicy(3)}

	err := adapter.StreamGenerate(context.Background(), "system", "user", func(string) error { return nil })

	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || providerErr.StatusCode != 401 ||
		providerErr.Retryable || model.calls != 1 {
		t.Fatalf("err=%#v calls=%d", err, model.calls)
	}
	if strings.Contains(err.Error(), "CANARY_PROVIDER_MESSAGE") {
		t.Fatalf("stream error leaked provider message: %s", err)
	}
}

func TestOpenAIAdapterStreamGenerateRetriesReceiveFailureBeforeContent(t *testing.T) {
	failedReader, failedWriter := schema.Pipe[*schema.Message](1)
	failedWriter.Send(nil, &einoopenai.APIError{HTTPStatusCode: 503})
	failedWriter.Close()
	model := &adapterTestChatModel{results: []streamTestResult{
		{stream: failedReader},
		{stream: schema.StreamReaderFromArray([]*schema.Message{
			schema.AssistantMessage("正文", nil),
		})},
	}}
	adapter := &OpenAIAdapter{chatModel: model, retryPolicy: noWaitRetryPolicy(3)}
	var chunks []string

	err := adapter.StreamGenerate(context.Background(), "system", "user", func(chunk string) error {
		chunks = append(chunks, chunk)
		return nil
	})

	if err != nil || model.calls != 2 || !slices.Equal(chunks, []string{"正文"}) {
		t.Fatalf("err=%v calls=%d chunks=%v", err, model.calls, chunks)
	}
}

func TestOpenAIAdapterStreamGenerateRetriesAfterEmptyContent(t *testing.T) {
	failedReader, failedWriter := schema.Pipe[*schema.Message](1)
	failedWriter.Send(
		schema.AssistantMessage("", nil),
		&einoopenai.APIError{HTTPStatusCode: 503},
	)
	failedWriter.Close()
	model := &adapterTestChatModel{results: []streamTestResult{
		{stream: failedReader},
		{stream: schema.StreamReaderFromArray([]*schema.Message{
			schema.AssistantMessage("正文", nil),
		})},
	}}
	adapter := &OpenAIAdapter{chatModel: model, retryPolicy: noWaitRetryPolicy(3)}
	var chunks []string

	err := adapter.StreamGenerate(context.Background(), "system", "user", func(chunk string) error {
		chunks = append(chunks, chunk)
		return nil
	})

	if err != nil || model.calls != 2 || !slices.Equal(chunks, []string{"正文"}) {
		t.Fatalf("err=%v calls=%d chunks=%v", err, model.calls, chunks)
	}
}

func TestOpenAIAdapterStreamGenerateStopsWhenBackoffIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	model := &adapterTestChatModel{err: &einoopenai.APIError{HTTPStatusCode: 429}}
	adapter := &OpenAIAdapter{
		chatModel: model,
		retryPolicy: retryPolicy{
			maxAttempts: 3,
			backoffs:    []time.Duration{time.Second},
			wait: func(context.Context, time.Duration) error {
				cancel()
				return context.Canceled
			},
		},
	}

	err := adapter.StreamGenerate(ctx, "system", "user", func(string) error { return nil })

	if !errors.Is(err, context.Canceled) || model.calls != 1 {
		t.Fatalf("err=%v calls=%d", err, model.calls)
	}
}

func TestOpenAIAdapterStreamGenerateDoesNotRetryAfterContent(t *testing.T) {
	failedReader, failedWriter := schema.Pipe[*schema.Message](1)
	failedWriter.Send(
		schema.AssistantMessage("部分正文", nil),
		&einoopenai.APIError{HTTPStatusCode: 429},
	)
	failedWriter.Close()
	model := &adapterTestChatModel{results: []streamTestResult{
		{stream: failedReader},
		{stream: schema.StreamReaderFromArray([]*schema.Message{
			schema.AssistantMessage("重复正文", nil),
		})},
	}}
	adapter := &OpenAIAdapter{chatModel: model, retryPolicy: noWaitRetryPolicy(3)}
	var chunks []string

	err := adapter.StreamGenerate(context.Background(), "system", "user", func(chunk string) error {
		chunks = append(chunks, chunk)
		return nil
	})

	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || providerErr.StatusCode != 429 ||
		model.calls != 1 || !slices.Equal(chunks, []string{"部分正文"}) {
		t.Fatalf("err=%#v calls=%d chunks=%v", err, model.calls, chunks)
	}
}

func TestOpenAIAdapterStreamGenerateReturnsStartError(t *testing.T) {
	startErr := errors.New("provider unavailable")
	adapter := &OpenAIAdapter{chatModel: &adapterTestChatModel{err: startErr}}

	err := adapter.StreamGenerate(context.Background(), "system", "user", func(string) error { return nil })
	if !errors.Is(err, startErr) {
		t.Fatalf("StreamGenerate() error = %v, want wrapped %v", err, startErr)
	}
}

func TestOpenAIAdapterStreamGenerateRejectsNilCallback(t *testing.T) {
	adapter := &OpenAIAdapter{}

	err := adapter.StreamGenerate(context.Background(), "system", "user", nil)
	if err == nil {
		t.Fatal("StreamGenerate() error = nil, want nil callback error")
	}
}

func TestOpenAIAdapterStreamGenerateCancelsBlockedReceive(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reader, writer := schema.Pipe[*schema.Message](0)
	adapter := &OpenAIAdapter{chatModel: &adapterTestChatModel{stream: reader}}

	result := make(chan error, 1)
	go func() {
		result <- adapter.StreamGenerate(ctx, "system", "user", func(string) error { return nil })
	}()

	cancel()
	go func() {
		writer.Send(nil, context.Canceled)
		writer.Close()
	}()

	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("StreamGenerate() error = %v, want context canceled", err)
	}
}

func TestOpenAIAdapterStreamGenerateProcessesContentBeforeReceiveError(t *testing.T) {
	receiveErr := errors.New("provider stream failed")
	reader, writer := schema.Pipe[*schema.Message](1)
	writer.Send(schema.AssistantMessage("最后一段", nil), receiveErr)
	writer.Close()
	adapter := &OpenAIAdapter{chatModel: &adapterTestChatModel{stream: reader}}

	var content string
	err := adapter.StreamGenerate(context.Background(), "system", "user", func(chunk string) error {
		content += chunk
		return nil
	})
	if !errors.Is(err, receiveErr) {
		t.Fatalf("StreamGenerate() error = %v, want wrapped %v", err, receiveErr)
	}
	if content != "最后一段" {
		t.Fatalf("content = %q, want %q", content, "最后一段")
	}
}
