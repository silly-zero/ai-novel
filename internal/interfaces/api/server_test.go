package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-novel/studio/internal/domain/agents"
)

type generationTestEngine struct {
	prepare func(context.Context, *agents.GenerationState) (*agents.GenerationState, error)
	run     func(context.Context, *agents.GenerationState) (*agents.GenerationState, error)
}

func (e *generationTestEngine) PrepareContext(ctx context.Context, state *agents.GenerationState) (*agents.GenerationState, error) {
	if e.prepare != nil {
		return e.prepare(ctx, state)
	}
	return state, nil
}

func (e *generationTestEngine) RunChapterGeneration(ctx context.Context, state *agents.GenerationState) (*agents.GenerationState, error) {
	if e.run != nil {
		return e.run(ctx, state)
	}
	return state, nil
}

func generateRequest(ctx context.Context, novelID string, chapterIndex int) *http.Request {
	url := fmt.Sprintf("/api/v1/novel/generate?novel_id=%s&chapter_index=%d&idea=test&persist=0", novelID, chapterIndex)
	return httptest.NewRequest(http.MethodGet, url, nil).WithContext(ctx)
}

func waitForSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for generation")
	}
}

func TestHandleGenerateChapterRejectsConcurrentRequestForSameNovel(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	engine := &generationTestEngine{run: func(_ context.Context, state *agents.GenerationState) (*agents.GenerationState, error) {
		close(entered)
		<-release
		return state, nil
	}}
	server := newServer(engine, nil)

	firstDone := make(chan struct{})
	go func() {
		server.HandleGenerateChapter(httptest.NewRecorder(), generateRequest(context.Background(), "7", 1))
		close(firstDone)
	}()
	waitForSignal(t, entered)

	second := httptest.NewRecorder()
	server.HandleGenerateChapter(second, generateRequest(context.Background(), "007", 2))
	if second.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", second.Code, http.StatusConflict)
	}
	if got := second.Header().Get("Content-Type"); strings.Contains(got, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want non-SSE conflict", got)
	}

	close(release)
	waitForSignal(t, firstDone)
}

func TestHandleGenerateChapterAllowsDifferentNovelsConcurrently(t *testing.T) {
	entered := make(chan string, 2)
	release := make(chan struct{})
	engine := &generationTestEngine{run: func(_ context.Context, state *agents.GenerationState) (*agents.GenerationState, error) {
		entered <- state.NovelID
		<-release
		return state, nil
	}}
	server := newServer(engine, nil)

	var wg sync.WaitGroup
	for _, novelID := range []string{"7", "8"} {
		wg.Go(func() {
			server.HandleGenerateChapter(httptest.NewRecorder(), generateRequest(context.Background(), novelID, 1))
		})
	}

	seen := map[string]bool{}
	for range 2 {
		select {
		case novelID := <-entered:
			seen[novelID] = true
		case <-time.After(time.Second):
			t.Fatal("different novels did not enter generation concurrently")
		}
	}
	if !seen["7"] || !seen["8"] {
		t.Fatalf("entered novels = %v, want 7 and 8", seen)
	}
	close(release)
	wg.Wait()
}

func TestHandleGenerateChapterKeepsLeaseUntilWorkerExitsAfterCancellation(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	engine := &generationTestEngine{run: func(_ context.Context, state *agents.GenerationState) (*agents.GenerationState, error) {
		close(entered)
		<-release
		return nil, errors.New("canceled")
	}}
	server := newServer(engine, nil)

	ctx, cancel := context.WithCancel(context.Background())
	firstDone := make(chan struct{})
	go func() {
		server.HandleGenerateChapter(httptest.NewRecorder(), generateRequest(ctx, "7", 1))
		close(firstDone)
	}()
	waitForSignal(t, entered)
	cancel()
	waitForSignal(t, firstDone)

	whileWorkerActive := httptest.NewRecorder()
	server.HandleGenerateChapter(whileWorkerActive, generateRequest(context.Background(), "7", 2))
	if whileWorkerActive.Code != http.StatusConflict {
		t.Fatalf("status while worker active = %d, want %d", whileWorkerActive.Code, http.StatusConflict)
	}

	close(release)
	deadline := time.Now().Add(time.Second)
	for {
		server.generationGuard.mu.Lock()
		_, active := server.generationGuard.active[7]
		server.generationGuard.mu.Unlock()
		if !active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("generation lease was not released after worker exit")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestHandleGenerateChapterReleasesLeaseAfterPrepareFailure(t *testing.T) {
	engine := &generationTestEngine{prepare: func(context.Context, *agents.GenerationState) (*agents.GenerationState, error) {
		return nil, errors.New("prepare failed")
	}}
	server := newServer(engine, nil)

	first := httptest.NewRecorder()
	server.HandleGenerateChapter(first, generateRequest(context.Background(), "7", 1))
	if !strings.Contains(first.Body.String(), "prepare failed") {
		t.Fatalf("first response = %q, want prepare error", first.Body.String())
	}

	second := httptest.NewRecorder()
	server.HandleGenerateChapter(second, generateRequest(context.Background(), "7", 2))
	if second.Code == http.StatusConflict {
		t.Fatal("prepare failure left novel lease active")
	}
}

func TestHandleGenerateChapterReleasesLeaseAfterSuccess(t *testing.T) {
	server := newServer(&generationTestEngine{}, nil)

	first := httptest.NewRecorder()
	server.HandleGenerateChapter(first, generateRequest(context.Background(), "7", 1))
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d, want %d", first.Code, http.StatusOK)
	}

	second := httptest.NewRecorder()
	server.HandleGenerateChapter(second, generateRequest(context.Background(), "7", 2))
	if second.Code != http.StatusOK {
		t.Fatalf("second status = %d, want %d", second.Code, http.StatusOK)
	}
}

func TestHandleGenerateChapterAttachesStreamSinkAfterPreparation(t *testing.T) {
	engine := &generationTestEngine{
		prepare: func(_ context.Context, state *agents.GenerationState) (*agents.GenerationState, error) {
			if state.StreamSink != nil {
				return nil, errors.New("StreamSink attached during preparation")
			}
			return state, nil
		},
		run: func(ctx context.Context, state *agents.GenerationState) (*agents.GenerationState, error) {
			if state.StreamSink == nil {
				return nil, errors.New("StreamSink missing during generation")
			}
			if err := state.StreamSink(ctx, agents.GenerationStreamEvent{
				Type:  agents.GenerationStreamEventToken,
				Token: "正文",
			}); err != nil {
				return nil, err
			}
			return state, nil
		},
	}
	server := newServer(engine, nil)
	recorder := httptest.NewRecorder()

	server.HandleGenerateChapter(recorder, generateRequest(context.Background(), "7", 1))

	if body := recorder.Body.String(); !strings.Contains(body, "正文") {
		t.Fatalf("SSE body does not contain generated token: %s", body)
	}
}

func TestHandleGenerateChapterStreamsAllTokensInOrder(t *testing.T) {
	const tokenCount = 150
	engine := &generationTestEngine{run: func(ctx context.Context, state *agents.GenerationState) (*agents.GenerationState, error) {
		for i := range tokenCount {
			if err := state.StreamSink(ctx, agents.GenerationStreamEvent{
				Type:  agents.GenerationStreamEventToken,
				Token: fmt.Sprintf("token-%03d", i),
			}); err != nil {
				return nil, err
			}
		}
		return state, nil
	}}
	server := newServer(engine, nil)
	recorder := httptest.NewRecorder()

	server.HandleGenerateChapter(recorder, generateRequest(context.Background(), "7", 1))

	body := recorder.Body.String()
	previous := -1
	for i := range tokenCount {
		token := fmt.Sprintf("token-%03d", i)
		position := strings.Index(body, token)
		if position == -1 {
			t.Fatalf("SSE body missing %q", token)
		}
		if position <= previous {
			t.Fatalf("%q position = %d, previous = %d", token, position, previous)
		}
		previous = position
	}
}

type blockingTokenResponseWriter struct {
	*httptest.ResponseRecorder
	blocked chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingTokenResponseWriter() *blockingTokenResponseWriter {
	return &blockingTokenResponseWriter{
		ResponseRecorder: httptest.NewRecorder(),
		blocked:          make(chan struct{}),
		release:          make(chan struct{}),
	}
}

func (w *blockingTokenResponseWriter) Write(data []byte) (int, error) {
	if strings.Contains(string(data), `"token"`) {
		w.once.Do(func() {
			close(w.blocked)
			<-w.release
		})
	}
	return w.ResponseRecorder.Write(data)
}

func (w *blockingTokenResponseWriter) Flush() {}

func TestHandleGenerateChapterBackpressuresSlowSSEWriter(t *testing.T) {
	secondAttempted := make(chan struct{})
	secondDelivered := make(chan struct{})
	engine := &generationTestEngine{run: func(ctx context.Context, state *agents.GenerationState) (*agents.GenerationState, error) {
		if err := state.StreamSink(ctx, agents.GenerationStreamEvent{
			Type:  agents.GenerationStreamEventToken,
			Token: "第一段",
		}); err != nil {
			return nil, err
		}
		close(secondAttempted)
		if err := state.StreamSink(ctx, agents.GenerationStreamEvent{
			Type:  agents.GenerationStreamEventToken,
			Token: "第二段",
		}); err != nil {
			return nil, err
		}
		close(secondDelivered)
		return state, nil
	}}
	server := newServer(engine, nil)
	writer := newBlockingTokenResponseWriter()
	handlerDone := make(chan struct{})
	go func() {
		server.HandleGenerateChapter(writer, generateRequest(context.Background(), "7", 1))
		close(handlerDone)
	}()

	waitForSignal(t, writer.blocked)
	waitForSignal(t, secondAttempted)
	select {
	case <-secondDelivered:
		t.Fatal("second token bypassed slow SSE writer")
	default:
	}

	close(writer.release)
	waitForSignal(t, secondDelivered)
	waitForSignal(t, handlerDone)
	body := writer.Body.String()
	first := strings.Index(body, "第一段")
	second := strings.Index(body, "第二段")
	if first == -1 || second == -1 || first >= second {
		t.Fatalf("SSE token order = first:%d second:%d, body: %s", first, second, body)
	}
}

func TestHandleGenerateChapterPreservesRetryAndTokenOrder(t *testing.T) {
	engine := &generationTestEngine{run: func(ctx context.Context, state *agents.GenerationState) (*agents.GenerationState, error) {
		events := []agents.GenerationStreamEvent{
			{Type: agents.GenerationStreamEventToken, Token: "旧稿正文"},
			{Type: agents.GenerationStreamEventRetry, RetryCount: 1, Critique: "需要重写"},
			{Type: agents.GenerationStreamEventToken, Token: "新版正文"},
		}
		for _, event := range events {
			if err := state.StreamSink(ctx, event); err != nil {
				return nil, err
			}
		}
		return state, nil
	}}
	server := newServer(engine, nil)
	recorder := httptest.NewRecorder()

	server.HandleGenerateChapter(recorder, generateRequest(context.Background(), "7", 1))

	body := recorder.Body.String()
	oldDraft := strings.Index(body, "旧稿正文")
	retry := strings.Index(body, "需要重写")
	newDraft := strings.Index(body, "新版正文")
	if oldDraft == -1 || retry == -1 || newDraft == -1 {
		t.Fatalf("SSE body missing ordered stream event: %s", body)
	}
	if !(oldDraft < retry && retry < newDraft) {
		t.Fatalf("stream event order = old:%d retry:%d new:%d", oldDraft, retry, newDraft)
	}
}

func TestHandleGenerateChapterCancellationUnblocksStreamSink(t *testing.T) {
	entered := make(chan struct{})
	exited := make(chan struct{})
	engine := &generationTestEngine{run: func(ctx context.Context, state *agents.GenerationState) (*agents.GenerationState, error) {
		close(entered)
		defer close(exited)
		for {
			if err := state.StreamSink(ctx, agents.GenerationStreamEvent{
				Type:  agents.GenerationStreamEventToken,
				Token: "正文",
			}); err != nil {
				return nil, err
			}
		}
	}}
	server := newServer(engine, nil)
	ctx, cancel := context.WithCancel(context.Background())
	handlerDone := make(chan struct{})
	go func() {
		server.HandleGenerateChapter(httptest.NewRecorder(), generateRequest(ctx, "7", 1))
		close(handlerDone)
	}()

	waitForSignal(t, entered)
	cancel()
	waitForSignal(t, handlerDone)
	waitForSignal(t, exited)

	deadline := time.Now().Add(time.Second)
	for {
		server.generationGuard.mu.Lock()
		_, active := server.generationGuard.active[7]
		server.generationGuard.mu.Unlock()
		if !active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("generation lease was not released after canceled sink exited")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestGenerationGuardReleaseRequiresMatchingGeneration(t *testing.T) {
	guard := newGenerationGuard()
	if !guard.acquire(7, "run-a") {
		t.Fatal("first acquire failed")
	}
	guard.release(7, "run-b")
	if guard.acquire(7, "run-c") {
		t.Fatal("mismatched release removed active generation")
	}
	guard.release(7, "run-a")
	if !guard.acquire(7, "run-c") {
		t.Fatal("matching release did not remove active generation")
	}
}
