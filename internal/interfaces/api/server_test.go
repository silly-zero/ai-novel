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

func TestHandleGenerateChapterEmitsSingleSuccessTerminal(t *testing.T) {
	server := newServer(&generationTestEngine{}, nil)
	recorder := httptest.NewRecorder()

	server.HandleGenerateChapter(
		recorder,
		generateRequest(context.Background(), "7", 1),
	)

	body := recorder.Body.String()
	if count := strings.Count(body, "event: terminal"); count != 1 {
		t.Fatalf("terminal count = %d, want 1; body: %s", count, body)
	}
	if !strings.Contains(body, `"status":"success"`) {
		t.Fatalf("SSE body missing success terminal: %s", body)
	}
	if strings.Contains(body, "event: end") || strings.Contains(body, "event: error") {
		t.Fatalf("SSE body contains legacy terminal: %s", body)
	}
}

func TestHandleGenerateChapterEmitsSingleErrorTerminal(t *testing.T) {
	engine := &generationTestEngine{run: func(
		context.Context,
		*agents.GenerationState,
	) (*agents.GenerationState, error) {
		return nil, errors.New("provider failed\nwith details")
	}}
	server := newServer(engine, nil)
	recorder := httptest.NewRecorder()

	server.HandleGenerateChapter(
		recorder,
		generateRequest(context.Background(), "7", 1),
	)

	body := recorder.Body.String()
	if count := strings.Count(body, "event: terminal"); count != 1 {
		t.Fatalf("terminal count = %d, want 1; body: %s", count, body)
	}
	if !strings.Contains(body, `"status":"error"`) ||
		!strings.Contains(body, `provider failed\nwith details`) {
		t.Fatalf("SSE body missing JSON error terminal: %s", body)
	}
	if strings.Contains(body, "event: end") || strings.Contains(body, "event: error") {
		t.Fatalf("SSE body contains legacy terminal: %s", body)
	}
}

func TestHandleGenerateChapterPrepareFailureUsesErrorTerminal(t *testing.T) {
	engine := &generationTestEngine{prepare: func(
		context.Context,
		*agents.GenerationState,
	) (*agents.GenerationState, error) {
		return nil, errors.New("prepare failed\nwith details")
	}}
	server := newServer(engine, nil)
	recorder := httptest.NewRecorder()

	server.HandleGenerateChapter(
		recorder,
		generateRequest(context.Background(), "7", 1),
	)

	body := recorder.Body.String()
	if count := strings.Count(body, "event: terminal"); count != 1 {
		t.Fatalf("terminal count = %d, want 1; body: %s", count, body)
	}
	if !strings.Contains(body, `"status":"error"`) ||
		!strings.Contains(body, `prepare failed\nwith details`) {
		t.Fatalf("SSE body missing prepare error terminal: %s", body)
	}
}

func TestHandleGenerateChapterTreatsNilFinalStateAsError(t *testing.T) {
	engine := &generationTestEngine{run: func(
		context.Context,
		*agents.GenerationState,
	) (*agents.GenerationState, error) {
		return nil, nil
	}}
	server := newServer(engine, nil)
	recorder := httptest.NewRecorder()

	server.HandleGenerateChapter(
		recorder,
		generateRequest(context.Background(), "7", 1),
	)

	body := recorder.Body.String()
	if !strings.Contains(body, `"status":"error"`) ||
		!strings.Contains(body, "generation returned no final state") {
		t.Fatalf("SSE body missing nil-state error terminal: %s", body)
	}
}

func TestHandleGenerateChapterUnknownStreamEventEndsWithError(t *testing.T) {
	engine := &generationTestEngine{run: func(
		ctx context.Context,
		state *agents.GenerationState,
	) (*agents.GenerationState, error) {
		if err := state.StreamSink(ctx, agents.GenerationStreamEvent{}); err != nil {
			return nil, err
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	server := newServer(engine, nil)
	recorder := httptest.NewRecorder()

	server.HandleGenerateChapter(
		recorder,
		generateRequest(context.Background(), "7", 1),
	)

	body := recorder.Body.String()
	if count := strings.Count(body, "event: terminal"); count != 1 {
		t.Fatalf("terminal count = %d, want 1; body: %s", count, body)
	}
	if !strings.Contains(body, `"status":"error"`) ||
		!strings.Contains(body, errGenerationProtocol.Error()) {
		t.Fatalf("SSE body missing protocol error terminal: %s", body)
	}
}

func activeGenerationID(
	t *testing.T,
	server *Server,
	novelID int,
) string {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		server.generationGuard.mu.Lock()
		active, exists := server.generationGuard.active[novelID]
		server.generationGuard.mu.Unlock()
		if exists {
			return active.generationID
		}
		if time.Now().After(deadline) {
			t.Fatal("generation did not become active")
		}
		time.Sleep(time.Millisecond)
	}
}

func cancelGenerationRequest(
	server *Server,
	novelID string,
	generationID string,
) *httptest.ResponseRecorder {
	body := fmt.Sprintf(`{"generation_id":%q}`, generationID)
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/novels/"+novelID+"/generate/cancel",
		strings.NewReader(body),
	)
	recorder := httptest.NewRecorder()
	server.router.ServeHTTP(recorder, req)
	return recorder
}

func TestCancelGenerationWaitsForWorkerBeforeCancelledTerminal(t *testing.T) {
	entered := make(chan struct{})
	workerRelease := make(chan struct{})
	var enteredOnce sync.Once
	engine := &generationTestEngine{run: func(
		ctx context.Context,
		state *agents.GenerationState,
	) (*agents.GenerationState, error) {
		firstRun := false
		enteredOnce.Do(func() {
			firstRun = true
			close(entered)
		})
		if !firstRun {
			return state, nil
		}
		<-ctx.Done()
		<-workerRelease
		return nil, ctx.Err()
	}}
	server := newServer(engine, nil)
	recorder := httptest.NewRecorder()
	handlerDone := make(chan struct{})
	go func() {
		server.HandleGenerateChapter(
			recorder,
			generateRequest(context.Background(), "7", 1),
		)
		close(handlerDone)
	}()
	waitForSignal(t, entered)
	generationID := activeGenerationID(t, server, 7)

	firstCancel := cancelGenerationRequest(server, "7", generationID)
	if firstCancel.Code != http.StatusAccepted {
		t.Fatalf("first cancel status = %d, want %d", firstCancel.Code, http.StatusAccepted)
	}
	secondCancel := cancelGenerationRequest(server, "7", generationID)
	if secondCancel.Code != http.StatusAccepted {
		t.Fatalf("second cancel status = %d, want %d", secondCancel.Code, http.StatusAccepted)
	}
	select {
	case <-handlerDone:
		t.Fatal("SSE ended before worker exited")
	default:
	}

	close(workerRelease)
	waitForSignal(t, handlerDone)
	body := recorder.Body.String()
	if count := strings.Count(body, "event: terminal"); count != 1 {
		t.Fatalf("terminal count = %d, want 1; body: %s", count, body)
	}
	if !strings.Contains(body, `"status":"cancelled"`) {
		t.Fatalf("SSE body missing cancelled terminal: %s", body)
	}

	next := httptest.NewRecorder()
	server.HandleGenerateChapter(
		next,
		generateRequest(context.Background(), "7", 2),
	)
	if next.Code == http.StatusConflict {
		t.Fatal("cancelled terminal arrived before generation lease release")
	}
}

func TestCancelGenerationWinsWhenEngineReturnsSuccessAfterCancellation(t *testing.T) {
	entered := make(chan struct{})
	engine := &generationTestEngine{run: func(
		ctx context.Context,
		state *agents.GenerationState,
	) (*agents.GenerationState, error) {
		close(entered)
		<-ctx.Done()
		return state, nil
	}}
	server := newServer(engine, nil)
	recorder := httptest.NewRecorder()
	handlerDone := make(chan struct{})
	go func() {
		server.HandleGenerateChapter(
			recorder,
			generateRequest(context.Background(), "7", 1),
		)
		close(handlerDone)
	}()
	waitForSignal(t, entered)
	generationID := activeGenerationID(t, server, 7)

	cancelResponse := cancelGenerationRequest(server, "7", generationID)
	if cancelResponse.Code != http.StatusAccepted {
		t.Fatalf("cancel status = %d, want %d", cancelResponse.Code, http.StatusAccepted)
	}
	waitForSignal(t, handlerDone)

	body := recorder.Body.String()
	if !strings.Contains(body, `"status":"cancelled"`) {
		t.Fatalf("SSE body missing cancelled terminal: %s", body)
	}
	if strings.Contains(body, `"status":"success"`) {
		t.Fatalf("cancellation was overwritten by successful engine return: %s", body)
	}
}

func TestCancelGenerationRejectsMissingAndMismatchedGeneration(t *testing.T) {
	server := newServer(&generationTestEngine{}, nil)

	missing := cancelGenerationRequest(server, "7", "run-missing")
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d, want %d", missing.Code, http.StatusNotFound)
	}

	ctx, cancel := testGenerationContext()
	if !server.generationGuard.acquire(7, "run-active", ctx, cancel) {
		t.Fatal("failed to register active generation")
	}
	mismatch := cancelGenerationRequest(server, "7", "run-stale")
	if mismatch.Code != http.StatusConflict {
		t.Fatalf("mismatch status = %d, want %d", mismatch.Code, http.StatusConflict)
	}
	server.generationGuard.release(7, "run-active")
}

func testGenerationContext() (context.Context, context.CancelCauseFunc) {
	return context.WithCancelCause(context.Background())
}

func TestGenerationGuardCancellationLinearizesWithRelease(t *testing.T) {
	guard := newGenerationGuard()
	ctx, cancel := testGenerationContext()
	if !guard.acquire(7, "run-a", ctx, cancel) {
		t.Fatal("acquire failed")
	}
	if got := guard.cancel(7, "run-a", errGenerationCancelled); got != generationCancelAccepted {
		t.Fatalf("cancel result = %v, want accepted", got)
	}
	cause := guard.finish(7, "run-a")
	if !errors.Is(cause, errGenerationCancelled) {
		t.Fatalf("finish cause = %v, want user cancellation", cause)
	}
	guard.release(7, "run-a")

	ctx, cancel = testGenerationContext()
	if !guard.acquire(7, "run-b", ctx, cancel) {
		t.Fatal("second acquire failed")
	}
	if cause := guard.finish(7, "run-b"); cause != nil {
		t.Fatalf("completed finish cause = %v, want nil", cause)
	}
	if got := guard.cancel(7, "run-b", errGenerationCancelled); got != generationCancelConflict {
		t.Fatalf("late cancel result = %v, want conflict", got)
	}
	guard.release(7, "run-b")
	if got := guard.cancel(7, "run-b", errGenerationCancelled); got != generationCancelNotFound {
		t.Fatalf("released cancel result = %v, want not found", got)
	}
}

func TestGenerationGuardFinishedGenerationRejectsLateCancellation(t *testing.T) {
	guard := newGenerationGuard()
	ctx, cancel := testGenerationContext()
	if !guard.acquire(7, "run-a", ctx, cancel) {
		t.Fatal("acquire failed")
	}
	if cause := guard.finish(7, "run-a"); cause != nil {
		t.Fatalf("finish cause = %v, want nil", cause)
	}
	if got := guard.cancel(7, "run-a", errGenerationCancelled); got != generationCancelConflict {
		t.Fatalf("late cancel result = %v, want conflict", got)
	}
	if guard.acquire(7, "run-b", ctx, cancel) {
		t.Fatal("finished generation released lease before explicit release")
	}
	guard.release(7, "run-a")
}

func TestGenerationGuardReleaseRequiresMatchingGeneration(t *testing.T) {
	guard := newGenerationGuard()
	ctxA, cancelA := testGenerationContext()
	if !guard.acquire(7, "run-a", ctxA, cancelA) {
		t.Fatal("first acquire failed")
	}
	guard.release(7, "run-b")
	ctxC, cancelC := testGenerationContext()
	if guard.acquire(7, "run-c", ctxC, cancelC) {
		t.Fatal("mismatched release removed active generation")
	}
	guard.release(7, "run-a")
	if !guard.acquire(7, "run-c", ctxC, cancelC) {
		t.Fatal("matching release did not remove active generation")
	}
}
