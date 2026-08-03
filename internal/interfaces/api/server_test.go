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
	"github.com/ai-novel/studio/internal/domain/events"
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

type generationTestBus struct {
	mu       sync.Mutex
	nextID   int
	handlers map[string]map[string]events.Handler
}

func newGenerationTestBus() *generationTestBus {
	return &generationTestBus{handlers: make(map[string]map[string]events.Handler)}
}

func (b *generationTestBus) Publish(ctx context.Context, event events.Event) error {
	b.mu.Lock()
	handlers := make([]events.Handler, 0, len(b.handlers[event.Topic()]))
	for _, handler := range b.handlers[event.Topic()] {
		handlers = append(handlers, handler)
	}
	b.mu.Unlock()
	for _, handler := range handlers {
		if err := handler(ctx, event); err != nil {
			return err
		}
	}
	return nil
}

func (b *generationTestBus) Subscribe(topic string, handler events.Handler) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextID++
	id := fmt.Sprintf("sub-%d", b.nextID)
	if b.handlers[topic] == nil {
		b.handlers[topic] = make(map[string]events.Handler)
	}
	b.handlers[topic][id] = handler
	return id
}

func (b *generationTestBus) Unsubscribe(topic, id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.handlers[topic], id)
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
	server := newServer(engine, newGenerationTestBus(), nil)

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
	server := newServer(engine, newGenerationTestBus(), nil)

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
	server := newServer(engine, newGenerationTestBus(), nil)

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
	server := newServer(engine, newGenerationTestBus(), nil)

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
	server := newServer(&generationTestEngine{}, newGenerationTestBus(), nil)

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

func TestHandleGenerateChapterFiltersEventsByGenerationID(t *testing.T) {
	bus := newGenerationTestBus()
	engine := &generationTestEngine{run: func(ctx context.Context, state *agents.GenerationState) (*agents.GenerationState, error) {
		if state.GenerationID == "" {
			return nil, errors.New("GenerationID is empty")
		}
		_ = bus.Publish(ctx, events.TokenGeneratedEvent{
			GenerationID: "stale-run",
			NovelID:      state.NovelID,
			Token:        "错误正文",
			Timestamp:    time.Now(),
		})
		_ = bus.Publish(ctx, events.ChapterRetryEvent{
			GenerationID: "stale-run",
			NovelID:      state.NovelID,
			RetryCount:   9,
			Critique:     "错误重试",
			Timestamp:    time.Now(),
		})
		_ = bus.Publish(ctx, events.TokenGeneratedEvent{
			GenerationID: state.GenerationID,
			NovelID:      state.NovelID,
			Token:        "正确正文",
			Timestamp:    time.Now(),
		})
		return state, nil
	}}
	server := newServer(engine, bus, nil)
	recorder := httptest.NewRecorder()

	server.HandleGenerateChapter(recorder, generateRequest(context.Background(), "7", 1))

	body := recorder.Body.String()
	if strings.Contains(body, "错误正文") || strings.Contains(body, "错误重试") {
		t.Fatalf("SSE body contains stale generation events: %s", body)
	}
	if !strings.Contains(body, "正确正文") {
		t.Fatalf("SSE body does not contain matching token: %s", body)
	}
	if !strings.Contains(body, `"generation_id":"`) {
		t.Fatalf("SSE metadata does not include generation_id: %s", body)
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
