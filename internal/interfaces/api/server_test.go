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

	"github.com/ai-novel/studio/ent"
	"github.com/ai-novel/studio/internal/domain/agents"
)

type generationTestEngine struct {
	prepare func(context.Context, *agents.GenerationState) (*agents.GenerationState, error)
	run     func(context.Context, *agents.GenerationState) (*agents.GenerationState, error)
	publish func(context.Context, *agents.GenerationState) error
	extract func(context.Context, *agents.GenerationState) (*agents.GenerationState, error)
}

type generationChapterStoreFake struct {
	mu           sync.Mutex
	prepareCalls int
	saveCalls    int
	prepare      func(context.Context, int, int, int) (*generationChapterTarget, error)
	save         func(context.Context, *generationChapterTarget, *agents.GenerationState) error
}

func (s *generationChapterStoreFake) Prepare(
	ctx context.Context,
	novelID int,
	chapterID int,
	chapterIndex int,
) (*generationChapterTarget, error) {
	s.mu.Lock()
	s.prepareCalls++
	s.mu.Unlock()
	if s.prepare != nil {
		return s.prepare(ctx, novelID, chapterID, chapterIndex)
	}
	return &generationChapterTarget{
		ID:        11,
		Title:     "旧标题",
		Content:   "旧正文",
		WordCount: 3,
		Order:     chapterIndex,
		Status:    "Draft",
		UpdatedAt: time.Unix(1, 0),
	}, nil
}

func (s *generationChapterStoreFake) Save(
	ctx context.Context,
	target *generationChapterTarget,
	state *agents.GenerationState,
) error {
	s.mu.Lock()
	s.saveCalls++
	s.mu.Unlock()
	if err := validateGenerationChapterSave(target, state); err != nil {
		return err
	}
	if s.save != nil {
		return s.save(ctx, target, state)
	}
	return nil
}

func (s *generationChapterStoreFake) calls() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.prepareCalls, s.saveCalls
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

func (e *generationTestEngine) ExtractContinuity(ctx context.Context, state *agents.GenerationState) (*agents.GenerationState, error) {
	if e.extract != nil {
		return e.extract(ctx, state)
	}
	state.Continuity = agents.ContinuityPacket{
		LastBeat:   "结尾动作",
		NextAction: "下一步动作",
	}
	return state, nil
}

func (e *generationTestEngine) PublishChapterGenerated(
	ctx context.Context,
	state *agents.GenerationState,
) error {
	if e.publish != nil {
		return e.publish(ctx, state)
	}
	return nil
}

func validGeneratedContent() string {
	return strings.Repeat("文", 2500)
}

func generateRequest(ctx context.Context, novelID string, chapterIndex int) *http.Request {
	return generateRequestWithPersist(ctx, novelID, chapterIndex, false)
}

func generateRequestWithPersist(
	ctx context.Context,
	novelID string,
	chapterIndex int,
	persist bool,
) *http.Request {
	persistValue := 0
	if persist {
		persistValue = 1
	}
	url := fmt.Sprintf(
		"/api/v1/novel/generate?novel_id=%s&chapter_index=%d&idea=test&persist=%d",
		novelID,
		chapterIndex,
		persistValue,
	)
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

func TestHTTPServerUsesConfiguredLocalBoundaries(t *testing.T) {
	cfg := defaultServerConfig()
	cfg.ListenAddr = "127.0.0.1:9091"
	cfg.ReadHeaderTimeout = 2 * time.Second
	cfg.ReadTimeout = 3 * time.Second
	cfg.WriteTimeout = 4 * time.Second
	cfg.IdleTimeout = 5 * time.Second
	server := newServerWithConfig(nil, nil, cfg).HTTPServer()

	if server.Addr != cfg.ListenAddr ||
		server.ReadHeaderTimeout != cfg.ReadHeaderTimeout ||
		server.ReadTimeout != cfg.ReadTimeout ||
		server.WriteTimeout != cfg.WriteTimeout ||
		server.IdleTimeout != cfg.IdleTimeout ||
		server.MaxHeaderBytes != 1<<20 {
		t.Fatalf("http server = %#v", server)
	}
}

func TestCORSMiddlewareAllowsConfiguredOriginAndRejectsUnknownPreflight(t *testing.T) {
	server := newServer(nil, nil)

	allowed := httptest.NewRequest(http.MethodOptions, "/api/v1/novels", nil)
	allowed.Header.Set("Origin", "http://127.0.0.1:5173")
	allowedRecorder := httptest.NewRecorder()
	server.router.ServeHTTP(allowedRecorder, allowed)
	if allowedRecorder.Code != http.StatusNoContent {
		t.Fatalf("allowed preflight status = %d", allowedRecorder.Code)
	}
	if origin := allowedRecorder.Header().Get("Access-Control-Allow-Origin"); origin != "http://127.0.0.1:5173" {
		t.Fatalf("allowed origin = %q", origin)
	}
	if vary := allowedRecorder.Header().Values("Vary"); len(vary) == 0 || !strings.Contains(strings.Join(vary, ","), "Origin") {
		t.Fatalf("Vary = %#v", vary)
	}

	unknown := httptest.NewRequest(http.MethodOptions, "/api/v1/novel/preview-context", nil)
	unknown.Header.Set("Origin", "https://example.com")
	unknownRecorder := httptest.NewRecorder()
	server.router.ServeHTTP(unknownRecorder, unknown)
	if unknownRecorder.Code != http.StatusForbidden {
		t.Fatalf("unknown preflight status = %d", unknownRecorder.Code)
	}
	if origin := unknownRecorder.Header().Get("Access-Control-Allow-Origin"); origin != "" {
		t.Fatalf("unknown origin was allowed: %q", origin)
	}
}

func TestCORSMiddlewareRejectsUnknownOriginBeforeGeneration(t *testing.T) {
	runCalled := false
	server := newServer(&generationTestEngine{
		run: func(context.Context, *agents.GenerationState) (*agents.GenerationState, error) {
			runCalled = true
			return nil, nil
		},
	}, nil)

	for _, setupHeaders := range []func(http.Header){
		func(header http.Header) { header.Set("Origin", "https://example.com") },
		func(header http.Header) { header.Set("Sec-Fetch-Site", "cross-site") },
	} {
		request := generateRequest(context.Background(), "7", 1)
		setupHeaders(request.Header)
		recorder := httptest.NewRecorder()
		server.router.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("cross-site generation status = %d, body=%s", recorder.Code, recorder.Body.String())
		}
	}
	if runCalled {
		t.Fatal("cross-site request invoked generation engine")
	}
}

func TestCORSMiddlewareAllowsGenerationWithoutBrowserOrigin(t *testing.T) {
	server := newServer(&generationTestEngine{}, nil)
	recorder := httptest.NewRecorder()
	server.router.ServeHTTP(recorder, generateRequest(context.Background(), "7", 1))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"status":"success"`) {
		t.Fatalf("local generation response = %d, body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestGlobalModelCapacityRejectsExcessGenerationAndReleasesSlot(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	engine := &generationTestEngine{run: func(ctx context.Context, state *agents.GenerationState) (*agents.GenerationState, error) {
		once.Do(func() { close(entered) })
		select {
		case <-release:
			return state, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	cfg := defaultServerConfig()
	cfg.MaxConcurrentGenerations = 1
	server := newServerWithConfig(engine, nil, cfg)

	firstDone := make(chan struct{})
	go func() {
		server.HandleGenerateChapter(httptest.NewRecorder(), generateRequest(context.Background(), "7", 1))
		close(firstDone)
	}()
	waitForSignal(t, entered)

	excess := httptest.NewRecorder()
	server.HandleGenerateChapter(excess, generateRequest(context.Background(), "8", 1))
	if excess.Code != http.StatusTooManyRequests || excess.Header().Get("Retry-After") != "1" {
		t.Fatalf("excess response = %d, headers=%v, body=%s", excess.Code, excess.Header(), excess.Body.String())
	}

	close(release)
	waitForSignal(t, firstDone)
	afterRelease := httptest.NewRecorder()
	server.HandleGenerateChapter(afterRelease, generateRequest(context.Background(), "8", 1))
	if afterRelease.Code != http.StatusOK || !strings.Contains(afterRelease.Body.String(), `"status":"success"`) {
		t.Fatalf("after release response = %d, body=%s", afterRelease.Code, afterRelease.Body.String())
	}
}

func TestPreviewUsesGlobalModelCapacity(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	engine := &generationTestEngine{prepare: func(ctx context.Context, state *agents.GenerationState) (*agents.GenerationState, error) {
		close(entered)
		select {
		case <-release:
			return state, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	cfg := defaultServerConfig()
	cfg.MaxConcurrentGenerations = 1
	server := newServerWithConfig(engine, nil, cfg)
	previewDone := make(chan struct{})
	go func() {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/api/v1/novel/preview-context?novel_id=7&idea=test", nil)
		server.HandlePreviewContext(recorder, request)
		close(previewDone)
	}()
	waitForSignal(t, entered)

	excess := httptest.NewRecorder()
	server.HandleGenerateChapter(excess, generateRequest(context.Background(), "8", 1))
	if excess.Code != http.StatusTooManyRequests {
		t.Fatalf("generation while preview active status = %d, body=%s", excess.Code, excess.Body.String())
	}
	close(release)
	waitForSignal(t, previewDone)
}

func TestGenerationOverallDeadlineProducesErrorTerminal(t *testing.T) {
	engine := &generationTestEngine{run: func(ctx context.Context, _ *agents.GenerationState) (*agents.GenerationState, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	cfg := defaultServerConfig()
	cfg.GenerationTimeout = 10 * time.Millisecond
	server := newServerWithConfig(engine, nil, cfg)
	recorder := httptest.NewRecorder()

	server.HandleGenerateChapter(recorder, generateRequest(context.Background(), "7", 1))
	if !strings.Contains(recorder.Body.String(), `"status":"error"`) ||
		!strings.Contains(recorder.Body.String(), "context deadline exceeded") {
		t.Fatalf("deadline SSE body = %s", recorder.Body.String())
	}
}

func TestClassifyGenerationResultPreservesCompletedStateAfterTransportCancellation(t *testing.T) {
	result := classifyGenerationResult(
		"generation-1",
		context.Canceled,
		nil,
		&agents.GenerationState{Draft: "完整正文"},
	)
	if result.Status != generationStatusSuccess {
		t.Fatalf("status = %s, want success", result.Status)
	}
}

func TestClassifyGenerationResultDoesNotOverrideDefinitiveCancellation(t *testing.T) {
	for _, cause := range []error{
		errGenerationCancelled,
		errGenerationProtocol,
		context.DeadlineExceeded,
	} {
		result := classifyGenerationResult(
			"generation-1",
			cause,
			nil,
			&agents.GenerationState{Draft: "不应保存"},
		)
		if result.Status == generationStatusSuccess {
			t.Fatalf("cause %v produced success", cause)
		}
	}
}

func TestGenerationOverallDeadlineCoversChapterSave(t *testing.T) {
	store := &generationChapterStoreFake{
		save: func(ctx context.Context, _ *generationChapterTarget, _ *agents.GenerationState) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}
	engine := &generationTestEngine{
		run: func(_ context.Context, state *agents.GenerationState) (*agents.GenerationState, error) {
			state.Draft = validGeneratedContent()
			state.IsApproved = true
			return state, nil
		},
	}
	cfg := defaultServerConfig()
	cfg.GenerationTimeout = 20 * time.Millisecond
	cfg.MaxConcurrentGenerations = 1
	server := newServerWithConfig(engine, nil, cfg)
	server.chapterStore = store
	recorder := httptest.NewRecorder()

	server.HandleGenerateChapter(
		recorder,
		generateRequestWithPersist(context.Background(), "7", 1, true),
	)
	if !strings.Contains(recorder.Body.String(), `"status":"error"`) ||
		!strings.Contains(recorder.Body.String(), "context deadline exceeded") {
		t.Fatalf("save deadline SSE body = %s", recorder.Body.String())
	}

	afterDeadline := httptest.NewRecorder()
	server.HandleGenerateChapter(afterDeadline, generateRequest(context.Background(), "8", 1))
	if afterDeadline.Code != http.StatusOK {
		t.Fatalf("capacity was not released after save deadline: status=%d body=%s", afterDeadline.Code, afterDeadline.Body.String())
	}
}

func TestClientDisconnectDoesNotCancelBoundedPostprocessing(t *testing.T) {
	saveContext := make(chan context.Context, 1)
	releaseSave := make(chan struct{})
	published := make(chan struct{})
	store := &generationChapterStoreFake{
		save: func(ctx context.Context, _ *generationChapterTarget, _ *agents.GenerationState) error {
			saveContext <- ctx
			<-releaseSave
			return ctx.Err()
		},
	}
	engine := &generationTestEngine{
		run: func(_ context.Context, state *agents.GenerationState) (*agents.GenerationState, error) {
			state.Draft = validGeneratedContent()
			state.IsApproved = true
			return state, nil
		},
		publish: func(ctx context.Context, _ *agents.GenerationState) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			close(published)
			return nil
		},
	}
	cfg := defaultServerConfig()
	cfg.GenerationTimeout = time.Second
	cfg.MaxConcurrentGenerations = 1
	server := newServerWithConfig(engine, nil, cfg)
	server.chapterStore = store
	requestCtx, disconnect := context.WithCancel(context.Background())
	handlerDone := make(chan struct{})
	go func() {
		server.HandleGenerateChapter(
			httptest.NewRecorder(),
			generateRequestWithPersist(requestCtx, "7", 1, true),
		)
		close(handlerDone)
	}()

	postprocessCtx := <-saveContext
	disconnect()
	waitForSignal(t, handlerDone)
	if err := postprocessCtx.Err(); err != nil {
		t.Fatalf("client disconnect cancelled postprocessing: %v", err)
	}

	sameNovel := httptest.NewRecorder()
	server.HandleGenerateChapter(sameNovel, generateRequest(context.Background(), "7", 2))
	if sameNovel.Code != http.StatusConflict {
		t.Fatalf("novel lease released during postprocessing: status=%d", sameNovel.Code)
	}
	differentNovel := httptest.NewRecorder()
	server.HandleGenerateChapter(differentNovel, generateRequest(context.Background(), "8", 1))
	if differentNovel.Code != http.StatusTooManyRequests {
		t.Fatalf("model slot released during postprocessing: status=%d", differentNovel.Code)
	}

	close(releaseSave)
	waitForSignal(t, published)
	deadline := time.Now().Add(time.Second)
	for {
		server.generationGuard.mu.Lock()
		_, active := server.generationGuard.active[7]
		server.generationGuard.mu.Unlock()
		if !active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("postprocessing completion did not release novel lease")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestShutdownCancelsActiveGenerationAndWaitsForCapacity(t *testing.T) {
	entered := make(chan struct{})
	exited := make(chan struct{})
	engine := &generationTestEngine{run: func(ctx context.Context, _ *agents.GenerationState) (*agents.GenerationState, error) {
		close(entered)
		<-ctx.Done()
		close(exited)
		return nil, ctx.Err()
	}}
	server := newServer(engine, nil)
	handlerDone := make(chan struct{})
	go func() {
		server.router.ServeHTTP(httptest.NewRecorder(), generateRequest(context.Background(), "7", 1))
		close(handlerDone)
	}()
	waitForSignal(t, entered)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx, server.HTTPServer()); err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}
	waitForSignal(t, exited)
	waitForSignal(t, handlerDone)
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

func TestGenerationPreviousChapterMissingErrorSupportsInspection(t *testing.T) {
	err := &generationPreviousChapterMissingError{
		NovelID:      7,
		MissingOrder: 2,
	}
	if !errors.Is(err, errGenerationPreviousChapterMissing) {
		t.Fatalf("errors.Is(%v) = false", err)
	}
	var typed *generationPreviousChapterMissingError
	if !errors.As(err, &typed) || typed.NovelID != 7 || typed.MissingOrder != 2 {
		t.Fatalf("typed error = %#v", typed)
	}
	if !strings.Contains(err.Error(), "chapter 2") || !strings.Contains(err.Error(), "novel 7") {
		t.Fatalf("error = %q", err)
	}
}

func TestPreparePreviousContinuityAllowsFirstChapterWithoutLookup(t *testing.T) {
	called := false
	packet, err := preparePreviousContinuity(
		context.Background(),
		7,
		1,
		func(context.Context, int, int) (*ent.Chapter, error) {
			called = true
			return nil, nil
		},
	)
	if err != nil || called || !packet.IsEmpty() {
		t.Fatalf("packet = %#v, called = %v, error = %v", packet, called, err)
	}
}

func TestPreparePreviousContinuityRequiresMissingChapter(t *testing.T) {
	packet, err := preparePreviousContinuity(
		context.Background(),
		7,
		3,
		func(_ context.Context, novelID int, order int) (*ent.Chapter, error) {
			if novelID != 7 || order != 2 {
				t.Fatalf("lookup = novel %d order %d", novelID, order)
			}
			return nil, &ent.NotFoundError{}
		},
	)
	if !packet.IsEmpty() || !errors.Is(err, errGenerationPreviousChapterMissing) {
		t.Fatalf("packet = %#v, error = %v", packet, err)
	}
	var typed *generationPreviousChapterMissingError
	if !errors.As(err, &typed) || typed.NovelID != 7 || typed.MissingOrder != 2 {
		t.Fatalf("typed error = %#v", typed)
	}
}

func TestPreparePreviousContinuityCopiesPreviousPacket(t *testing.T) {
	loops := []string{"线索一", "线索二"}
	packet, err := preparePreviousContinuity(
		context.Background(),
		7,
		3,
		func(context.Context, int, int) (*ent.Chapter, error) {
			return &ent.Chapter{
				LastBeat:   "  最后动作  ",
				OpenLoops:  loops,
				NextAction: "  继续追查  ",
			}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if packet.LastBeat != "最后动作" || packet.NextAction != "继续追查" || len(packet.OpenLoops) != 2 {
		t.Fatalf("packet = %#v", packet)
	}
	packet.OpenLoops[0] = "已修改"
	if loops[0] != "线索一" {
		t.Fatalf("lookup loops shared backing array: %#v", loops)
	}
}

func TestPreparePreviousContinuityRejectsInvalidPacket(t *testing.T) {
	_, err := preparePreviousContinuity(
		context.Background(),
		7,
		3,
		func(context.Context, int, int) (*ent.Chapter, error) {
			return &ent.Chapter{}, nil
		},
	)
	if err == nil || !strings.Contains(err.Error(), "last_beat is required") {
		t.Fatalf("error = %v", err)
	}
}

func TestPreparePreviousContinuityPropagatesLookupError(t *testing.T) {
	lookupErr := errors.New("lookup failed")
	_, err := preparePreviousContinuity(
		context.Background(),
		7,
		3,
		func(context.Context, int, int) (*ent.Chapter, error) {
			return nil, lookupErr
		},
	)
	if !errors.Is(err, lookupErr) {
		t.Fatalf("error = %v, want %v", err, lookupErr)
	}
}

func TestPrepareNewGenerationChapterChecksPredecessorBeforeCreate(t *testing.T) {
	createCalled := false
	_, err := prepareNewGenerationChapter(
		context.Background(),
		7,
		3,
		func(context.Context, int) error {
			return nil
		},
		func(context.Context, int, int) (*ent.Chapter, error) {
			return nil, &ent.NotFoundError{}
		},
		func(context.Context, int, int) (*ent.Chapter, error) {
			return nil, &ent.NotFoundError{}
		},
		func(context.Context, int, int) (*ent.Chapter, error) {
			createCalled = true
			return nil, nil
		},
	)
	if !errors.Is(err, errGenerationPreviousChapterMissing) {
		t.Fatalf("error = %v", err)
	}
	if createCalled {
		t.Fatal("target chapter was created before predecessor validation")
	}
}

func TestPrepareNewGenerationChapterAttachesPreviousContinuity(t *testing.T) {
	var events []string
	target, err := prepareNewGenerationChapter(
		context.Background(),
		7,
		3,
		func(context.Context, int) error {
			events = append(events, "lock")
			return nil
		},
		func(context.Context, int, int) (*ent.Chapter, error) {
			events = append(events, "target")
			return nil, &ent.NotFoundError{}
		},
		func(context.Context, int, int) (*ent.Chapter, error) {
			events = append(events, "lookup")
			return &ent.Chapter{LastBeat: "结尾", NextAction: "行动"}, nil
		},
		func(_ context.Context, novelID int, order int) (*ent.Chapter, error) {
			events = append(events, "create")
			return &ent.Chapter{ID: 11, Order: order}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(events, ",") != "lock,target,lookup,create" || target.ID != 11 || target.Order != 3 || target.PreviousContinuity.LastBeat != "结尾" {
		t.Fatalf("events = %v, target = %#v", events, target)
	}
}

func TestPrepareNewGenerationChapterReusesTargetAfterLock(t *testing.T) {
	createCalled := false
	target, err := prepareNewGenerationChapter(
		context.Background(),
		7,
		3,
		func(context.Context, int) error { return nil },
		func(context.Context, int, int) (*ent.Chapter, error) {
			return &ent.Chapter{ID: 11, Order: 3}, nil
		},
		func(context.Context, int, int) (*ent.Chapter, error) {
			return &ent.Chapter{LastBeat: "结尾", NextAction: "行动"}, nil
		},
		func(context.Context, int, int) (*ent.Chapter, error) {
			createCalled = true
			return nil, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if createCalled || target.ID != 11 || target.PreviousContinuity.LastBeat != "结尾" {
		t.Fatalf("create called = %v, target = %#v", createCalled, target)
	}
}

func TestPrepareNewGenerationChapterStopsAtNovelLockFailure(t *testing.T) {
	lockErr := errors.New("lock failed")
	lookupCalled := false
	_, err := prepareNewGenerationChapter(
		context.Background(),
		7,
		3,
		func(context.Context, int) error { return lockErr },
		func(context.Context, int, int) (*ent.Chapter, error) {
			lookupCalled = true
			return nil, nil
		},
		func(context.Context, int, int) (*ent.Chapter, error) {
			lookupCalled = true
			return nil, nil
		},
		func(context.Context, int, int) (*ent.Chapter, error) {
			lookupCalled = true
			return nil, nil
		},
	)
	if !errors.Is(err, lockErr) || lookupCalled {
		t.Fatalf("error = %v, downstream called = %v", err, lookupCalled)
	}
}

func TestIsChapterIntegrityConflict(t *testing.T) {
	conflicts := []error{
		&generationPreviousChapterMissingError{NovelID: 7, MissingOrder: 2},
		fmt.Errorf("wrapped: %w", errChapterOrderOccupied),
		errChapterHasSuccessor,
	}
	for _, err := range conflicts {
		if !isChapterIntegrityConflict(err) {
			t.Fatalf("isChapterIntegrityConflict(%v) = false", err)
		}
	}
	for _, err := range []error{nil, errors.New("database unavailable"), &ent.NotSingularError{}} {
		if isChapterIntegrityConflict(err) {
			t.Fatalf("isChapterIntegrityConflict(%v) = true", err)
		}
	}
}

func TestChapterMutationHTTPStatus(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
	}{
		{name: "not found", err: fmt.Errorf("wrapped: %w", &ent.NotFoundError{}), status: http.StatusNotFound},
		{name: "missing predecessor", err: &generationPreviousChapterMissingError{NovelID: 7, MissingOrder: 2}, status: http.StatusConflict},
		{name: "occupied order", err: errChapterOrderOccupied, status: http.StatusConflict},
		{name: "has successor", err: fmt.Errorf("wrapped: %w", errChapterHasSuccessor), status: http.StatusConflict},
		{name: "database failure", err: errors.New("database unavailable"), status: http.StatusInternalServerError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if status := chapterMutationHTTPStatus(test.err); status != test.status {
				t.Fatalf("status = %d, want %d", status, test.status)
			}
		})
	}
}

func TestRequireAvailableChapterOrder(t *testing.T) {
	lookupErr := errors.New("lookup failed")
	tests := []struct {
		name             string
		currentChapterID int
		row              *ent.Chapter
		lookupErr        error
		wantErr          error
	}{
		{name: "available", lookupErr: &ent.NotFoundError{}},
		{name: "current chapter", currentChapterID: 11, row: &ent.Chapter{ID: 11}},
		{name: "occupied", currentChapterID: 11, row: &ent.Chapter{ID: 12}, wantErr: errChapterOrderOccupied},
		{name: "not singular", lookupErr: &ent.NotSingularError{}, wantErr: &ent.NotSingularError{}},
		{name: "lookup failure", lookupErr: lookupErr, wantErr: lookupErr},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := requireAvailableChapterOrder(
				context.Background(),
				7,
				3,
				test.currentChapterID,
				func(_ context.Context, novelID, order int) (*ent.Chapter, error) {
					if novelID != 7 || order != 3 {
						t.Fatalf("lookup = novel %d order %d", novelID, order)
					}
					return test.row, test.lookupErr
				},
			)
			switch {
			case test.wantErr == nil && err != nil:
				t.Fatalf("error = %v, want nil", err)
			case errors.Is(test.wantErr, lookupErr) && !errors.Is(err, lookupErr):
				t.Fatalf("error = %v, want %v", err, lookupErr)
			case ent.IsNotSingular(test.wantErr) && !ent.IsNotSingular(err):
				t.Fatalf("error = %v, want not singular", err)
			case test.wantErr != nil && !errors.Is(err, test.wantErr) && !ent.IsNotSingular(test.wantErr):
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestRequirePreviousChapterOrder(t *testing.T) {
	lookupErr := errors.New("lookup failed")
	tests := []struct {
		name             string
		order            int
		currentChapterID int
		row              *ent.Chapter
		lookupErr        error
		wantMissingOrder int
		wantErr          error
		wantLookup       bool
	}{
		{name: "first chapter", order: 1},
		{name: "previous exists", order: 3, row: &ent.Chapter{ID: 10}, wantLookup: true},
		{name: "previous missing", order: 3, lookupErr: &ent.NotFoundError{}, wantMissingOrder: 2, wantLookup: true},
		{name: "current chapter is previous", order: 4, currentChapterID: 11, row: &ent.Chapter{ID: 11}, wantMissingOrder: 3, wantLookup: true},
		{name: "not singular", order: 3, lookupErr: &ent.NotSingularError{}, wantErr: &ent.NotSingularError{}, wantLookup: true},
		{name: "lookup failure", order: 3, lookupErr: lookupErr, wantErr: lookupErr, wantLookup: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called := false
			err := requirePreviousChapterOrder(
				context.Background(),
				7,
				test.order,
				test.currentChapterID,
				func(_ context.Context, novelID, order int) (*ent.Chapter, error) {
					called = true
					if novelID != 7 || order != test.order-1 {
						t.Fatalf("lookup = novel %d order %d", novelID, order)
					}
					return test.row, test.lookupErr
				},
			)
			if called != test.wantLookup {
				t.Fatalf("lookup called = %v, want %v", called, test.wantLookup)
			}
			if test.wantMissingOrder > 0 {
				var missing *generationPreviousChapterMissingError
				if !errors.As(err, &missing) || missing.NovelID != 7 || missing.MissingOrder != test.wantMissingOrder {
					t.Fatalf("error = %v, typed = %#v", err, missing)
				}
				return
			}
			switch {
			case test.wantErr == nil && err != nil:
				t.Fatalf("error = %v, want nil", err)
			case errors.Is(test.wantErr, lookupErr) && !errors.Is(err, lookupErr):
				t.Fatalf("error = %v, want %v", err, lookupErr)
			case ent.IsNotSingular(test.wantErr) && !ent.IsNotSingular(err):
				t.Fatalf("error = %v, want not singular", err)
			}
		})
	}
}

func TestRequireNoChapterSuccessor(t *testing.T) {
	lookupErr := errors.New("lookup failed")
	tests := []struct {
		name      string
		row       *ent.Chapter
		lookupErr error
		wantErr   error
	}{
		{name: "no successor", lookupErr: &ent.NotFoundError{}},
		{name: "has successor", row: &ent.Chapter{ID: 12}, wantErr: errChapterHasSuccessor},
		{name: "not singular", lookupErr: &ent.NotSingularError{}, wantErr: &ent.NotSingularError{}},
		{name: "lookup failure", lookupErr: lookupErr, wantErr: lookupErr},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := requireNoChapterSuccessor(
				context.Background(),
				7,
				3,
				func(_ context.Context, novelID, order int) (*ent.Chapter, error) {
					if novelID != 7 || order != 4 {
						t.Fatalf("lookup = novel %d order %d", novelID, order)
					}
					return test.row, test.lookupErr
				},
			)
			switch {
			case test.wantErr == nil && err != nil:
				t.Fatalf("error = %v, want nil", err)
			case errors.Is(test.wantErr, lookupErr) && !errors.Is(err, lookupErr):
				t.Fatalf("error = %v, want %v", err, lookupErr)
			case ent.IsNotSingular(test.wantErr) && !ent.IsNotSingular(err):
				t.Fatalf("error = %v, want not singular", err)
			case test.wantErr != nil && !errors.Is(err, test.wantErr) && !ent.IsNotSingular(test.wantErr):
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestEntGenerationChapterStoreRejectsInvalidTarget(t *testing.T) {
	store := &entGenerationChapterStore{}
	if _, err := store.Prepare(context.Background(), 0, 0, 1); err == nil ||
		err.Error() != "invalid novel id" {
		t.Fatalf("invalid novel error = %v", err)
	}
	if _, err := store.Prepare(context.Background(), 7, 0, 0); err == nil ||
		err.Error() != "invalid chapter index" {
		t.Fatalf("invalid chapter index error = %v", err)
	}
}

func TestEntGenerationChapterStoreRejectsInvalidSaveBeforeMutation(t *testing.T) {
	store := &entGenerationChapterStore{}
	validTarget := &generationChapterTarget{ID: 11}
	validState := &agents.GenerationState{
		Draft:      strings.Repeat("文", 2500),
		IsApproved: true,
	}
	tests := []struct {
		name    string
		target  *generationChapterTarget
		state   *agents.GenerationState
		wantErr string
	}{
		{name: "nil target", state: validState, wantErr: "target is nil"},
		{name: "nil state", target: validTarget, wantErr: "state is nil"},
		{
			name:    "unapproved state",
			target:  validTarget,
			state:   &agents.GenerationState{Draft: strings.Repeat("文", 2500)},
			wantErr: "not approved",
		},
		{
			name:    "invalid content",
			target:  validTarget,
			state:   &agents.GenerationState{Draft: strings.Repeat("文", 2500) + "【场景卡】", IsApproved: true},
			wantErr: "prompt_label_leak",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := store.Save(context.Background(), test.target, test.state)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Save() error = %v, want %q", err, test.wantErr)
			}
			if test.state != nil && strings.Contains(err.Error(), test.state.Draft) {
				t.Fatalf("Save() error leaked draft content: %q", err)
			}
		})
	}
}

func TestValidateGenerationChapterSaveAllowsApprovedValidContent(t *testing.T) {
	err := validateGenerationChapterSave(
		&generationChapterTarget{ID: 11},
		&agents.GenerationState{
			Draft:      strings.Repeat("文", 2500),
			IsApproved: true,
			Continuity: agents.ContinuityPacket{
				LastBeat:   "结尾动作",
				NextAction: "下一步动作",
			},
		},
	)
	if err != nil {
		t.Fatalf("validateGenerationChapterSave() error = %v", err)
	}
}

func TestValidateGenerationChapterSaveRejectsInvalidContinuity(t *testing.T) {
	err := validateGenerationChapterSave(
		&generationChapterTarget{ID: 11},
		&agents.GenerationState{
			Draft:      strings.Repeat("文", 2500),
			IsApproved: true,
			Continuity: agents.ContinuityPacket{LastBeat: "结尾"},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "next_action is required") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateGenerationChapterSavePreservesContentErrorPriority(t *testing.T) {
	err := validateGenerationChapterSave(
		&generationChapterTarget{ID: 11},
		&agents.GenerationState{
			Draft:      "短正文",
			IsApproved: true,
			Continuity: agents.ContinuityPacket{},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "content_too_short") || strings.Contains(err.Error(), "last_beat") {
		t.Fatalf("error = %v", err)
	}
}

func TestHandleGenerateChapterPersistRequiresChapterStore(t *testing.T) {
	runCalled := false
	engine := &generationTestEngine{
		run: func(context.Context, *agents.GenerationState) (*agents.GenerationState, error) {
			runCalled = true
			return nil, nil
		},
	}
	server := newServer(engine, nil)
	recorder := httptest.NewRecorder()

	server.HandleGenerateChapter(
		recorder,
		generateRequestWithPersist(context.Background(), "7", 1, true),
	)

	body := recorder.Body.String()
	if !strings.Contains(body, `"status":"error"`) ||
		!strings.Contains(body, "database not configured") {
		t.Fatalf("SSE body missing database error terminal: %s", body)
	}
	if runCalled {
		t.Fatal("generation ran without a configured chapter store")
	}
}

func TestHandleGenerateChapterMissingPreviousChapterFailsClosed(t *testing.T) {
	missingErr := &generationPreviousChapterMissingError{NovelID: 7, MissingOrder: 2}
	store := &generationChapterStoreFake{
		prepare: func(context.Context, int, int, int) (*generationChapterTarget, error) {
			return nil, missingErr
		},
	}
	prepareContextCalls := 0
	runCalls := 0
	extractCalls := 0
	publishCalls := 0
	engine := &generationTestEngine{
		prepare: func(context.Context, *agents.GenerationState) (*agents.GenerationState, error) {
			prepareContextCalls++
			return nil, errors.New("PrepareContext must not run")
		},
		run: func(context.Context, *agents.GenerationState) (*agents.GenerationState, error) {
			runCalls++
			return nil, errors.New("RunChapterGeneration must not run")
		},
		extract: func(context.Context, *agents.GenerationState) (*agents.GenerationState, error) {
			extractCalls++
			return nil, errors.New("ExtractContinuity must not run")
		},
		publish: func(context.Context, *agents.GenerationState) error {
			publishCalls++
			return errors.New("PublishChapterGenerated must not run")
		},
	}
	server := newServer(engine, nil)
	server.chapterStore = store

	first := httptest.NewRecorder()
	server.HandleGenerateChapter(
		first,
		generateRequestWithPersist(context.Background(), "7", 3, true),
	)
	body := first.Body.String()
	if count := strings.Count(body, "event: terminal"); count != 1 {
		t.Fatalf("terminal count = %d, want 1; body: %s", count, body)
	}
	if !strings.Contains(body, `"status":"error"`) ||
		!strings.Contains(body, "chapter 2") ||
		!strings.Contains(body, "novel 7") {
		t.Fatalf("SSE body missing missing-predecessor error: %s", body)
	}
	if prepareContextCalls != 0 || runCalls != 0 || extractCalls != 0 || publishCalls != 0 {
		t.Fatalf("downstream calls = prepare %d run %d extract %d publish %d", prepareContextCalls, runCalls, extractCalls, publishCalls)
	}
	prepareCalls, saveCalls := store.calls()
	if prepareCalls != 1 || saveCalls != 0 {
		t.Fatalf("store calls = (%d, %d), want (1, 0)", prepareCalls, saveCalls)
	}

	second := httptest.NewRecorder()
	server.HandleGenerateChapter(
		second,
		generateRequestWithPersist(context.Background(), "7", 3, true),
	)
	if second.Code != http.StatusOK {
		t.Fatalf("second status = %d, want %d", second.Code, http.StatusOK)
	}
	prepareCalls, saveCalls = store.calls()
	if prepareCalls != 2 || saveCalls != 0 {
		t.Fatalf("store calls after retry = (%d, %d), want (2, 0)", prepareCalls, saveCalls)
	}
}

func TestHandleGenerateChapterPersistZeroSkipsChapterStoreAndEvent(t *testing.T) {
	store := &generationChapterStoreFake{}
	published := false
	engine := &generationTestEngine{
		run: func(_ context.Context, state *agents.GenerationState) (*agents.GenerationState, error) {
			state.Draft = validGeneratedContent()
			state.IsApproved = true
			return state, nil
		},
		publish: func(context.Context, *agents.GenerationState) error {
			published = true
			return nil
		},
	}
	server := newServer(engine, nil)
	server.chapterStore = store
	recorder := httptest.NewRecorder()

	server.HandleGenerateChapter(
		recorder,
		generateRequest(context.Background(), "7", 3),
	)

	prepareCalls, saveCalls := store.calls()
	if prepareCalls != 0 || saveCalls != 0 {
		t.Fatalf("store calls = (%d, %d), want (0, 0)", prepareCalls, saveCalls)
	}
	if published {
		t.Fatal("persist=0 published chapter.generated")
	}
	if !strings.Contains(recorder.Body.String(), `"status":"success"`) {
		t.Fatalf("SSE body missing success terminal: %s", recorder.Body.String())
	}
}

func TestHandleGenerateChapterUsesPersistedOrderForChapterIDRegeneration(t *testing.T) {
	var generatedIndex int
	store := &generationChapterStoreFake{
		prepare: func(context.Context, int, int, int) (*generationChapterTarget, error) {
			return &generationChapterTarget{
				ID:        11,
				Title:     "第四章",
				Order:     4,
				Status:    "Draft",
				UpdatedAt: time.Unix(1, 0),
			}, nil
		},
	}
	engine := &generationTestEngine{
		prepare: func(_ context.Context, state *agents.GenerationState) (*agents.GenerationState, error) {
			generatedIndex = state.ChapterIndex
			return state, nil
		},
		run: func(_ context.Context, state *agents.GenerationState) (*agents.GenerationState, error) {
			state.Draft = validGeneratedContent()
			state.IsApproved = true
			return state, nil
		},
	}
	server := newServer(engine, nil)
	server.chapterStore = store
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/novel/generate?novel_id=7&chapter_id=11&idea=test&persist=1",
		nil,
	)

	server.HandleGenerateChapter(recorder, request)

	if generatedIndex != 4 {
		t.Fatalf("chapter index = %d, want persisted order 4", generatedIndex)
	}
	if !strings.Contains(recorder.Body.String(), `"status":"success"`) {
		t.Fatalf("generation failed: %s", recorder.Body.String())
	}
}

func TestHandleGenerateChapterCancelledPersistedRunDoesNotSave(t *testing.T) {
	entered := make(chan struct{})
	store := &generationChapterStoreFake{}
	engine := &generationTestEngine{
		run: func(ctx context.Context, state *agents.GenerationState) (*agents.GenerationState, error) {
			close(entered)
			<-ctx.Done()
			state.Draft = "不应保存的正文"
			return state, nil
		},
	}
	server := newServer(engine, nil)
	server.chapterStore = store
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		server.HandleGenerateChapter(
			recorder,
			generateRequestWithPersist(context.Background(), "7", 1, true),
		)
		close(done)
	}()
	waitForSignal(t, entered)

	generationID := activeGenerationID(t, server, 7)
	cancelResponse := cancelGenerationRequest(server, "7", generationID)
	if cancelResponse.Code != http.StatusAccepted {
		t.Fatalf("cancel status = %d, want %d", cancelResponse.Code, http.StatusAccepted)
	}
	waitForSignal(t, done)

	prepareCalls, saveCalls := store.calls()
	if prepareCalls != 1 || saveCalls != 0 {
		t.Fatalf("store calls = (%d, %d), want (1, 0)", prepareCalls, saveCalls)
	}
	if !strings.Contains(recorder.Body.String(), `"status":"cancelled"`) {
		t.Fatalf("SSE body missing cancelled terminal: %s", recorder.Body.String())
	}
}

func TestHandleGenerateChapterFailurePreservesPreparedChapter(t *testing.T) {
	target := &generationChapterTarget{
		ID:        11,
		Title:     "原章节",
		Content:   "不能丢失的正文",
		WordCount: 7,
		Order:     1,
		Status:    "Published",
		UpdatedAt: time.Unix(1, 0),
	}
	store := &generationChapterStoreFake{
		prepare: func(context.Context, int, int, int) (*generationChapterTarget, error) {
			return target, nil
		},
	}
	engine := &generationTestEngine{
		run: func(context.Context, *agents.GenerationState) (*agents.GenerationState, error) {
			return nil, errors.New("provider failed")
		},
	}
	server := newServer(engine, nil)
	server.chapterStore = store
	recorder := httptest.NewRecorder()

	server.HandleGenerateChapter(
		recorder,
		generateRequestWithPersist(context.Background(), "7", 1, true),
	)

	_, saveCalls := store.calls()
	if saveCalls != 0 {
		t.Fatalf("save calls = %d, want 0", saveCalls)
	}
	if target.Content != "不能丢失的正文" || target.Status != "Published" {
		t.Fatalf("prepared target was modified: %#v", target)
	}
	if !strings.Contains(recorder.Body.String(), `"status":"error"`) {
		t.Fatalf("SSE body missing error terminal: %s", recorder.Body.String())
	}
}

func TestHandleGenerateChapterInvalidFinalStateDoesNotPublish(t *testing.T) {
	published := false
	store := &generationChapterStoreFake{}
	engine := &generationTestEngine{
		run: func(_ context.Context, state *agents.GenerationState) (*agents.GenerationState, error) {
			state.Draft = strings.Repeat("文", 2500) + "【场景卡】"
			state.IsApproved = true
			return state, nil
		},
		publish: func(context.Context, *agents.GenerationState) error {
			published = true
			return nil
		},
	}
	server := newServer(engine, nil)
	server.chapterStore = store
	recorder := httptest.NewRecorder()

	server.HandleGenerateChapter(
		recorder,
		generateRequestWithPersist(context.Background(), "7", 1, true),
	)

	body := recorder.Body.String()
	if !strings.Contains(body, `"status":"error"`) ||
		!strings.Contains(body, "prompt_label_leak") {
		t.Fatalf("SSE body missing content validation error: %s", body)
	}
	if published {
		t.Fatal("invalid final state published chapter.generated")
	}
	_, saveCalls := store.calls()
	if saveCalls != 1 {
		t.Fatalf("save calls = %d, want 1", saveCalls)
	}
}

func TestHandleGenerateChapterSaveFailureUsesErrorTerminal(t *testing.T) {
	published := false
	store := &generationChapterStoreFake{
		save: func(context.Context, *generationChapterTarget, *agents.GenerationState) error {
			return errors.New("database unavailable")
		},
	}
	engine := &generationTestEngine{
		run: func(_ context.Context, state *agents.GenerationState) (*agents.GenerationState, error) {
			state.Draft = validGeneratedContent()
			state.IsApproved = true
			return state, nil
		},
		publish: func(context.Context, *agents.GenerationState) error {
			published = true
			return nil
		},
	}
	server := newServer(engine, nil)
	server.chapterStore = store
	recorder := httptest.NewRecorder()

	server.HandleGenerateChapter(
		recorder,
		generateRequestWithPersist(context.Background(), "7", 1, true),
	)

	body := recorder.Body.String()
	if count := strings.Count(body, "event: terminal"); count != 1 {
		t.Fatalf("terminal count = %d, want 1; body: %s", count, body)
	}
	if !strings.Contains(body, `"status":"error"`) ||
		!strings.Contains(body, "save generated chapter: database unavailable") {
		t.Fatalf("SSE body missing save error terminal: %s", body)
	}
	if strings.Contains(body, `"status":"success"`) {
		t.Fatalf("SSE body contains success after save failure: %s", body)
	}
	if published {
		t.Fatal("save failure published chapter.generated")
	}
}

func TestHandleGenerateChapterCASConflictPreservesConcurrentEdit(t *testing.T) {
	store := &generationChapterStoreFake{
		save: func(context.Context, *generationChapterTarget, *agents.GenerationState) error {
			return errGenerationChapterChanged
		},
	}
	engine := &generationTestEngine{
		run: func(_ context.Context, state *agents.GenerationState) (*agents.GenerationState, error) {
			state.Draft = validGeneratedContent()
			state.IsApproved = true
			return state, nil
		},
	}
	server := newServer(engine, nil)
	server.chapterStore = store
	recorder := httptest.NewRecorder()

	server.HandleGenerateChapter(
		recorder,
		generateRequestWithPersist(context.Background(), "7", 1, true),
	)

	body := recorder.Body.String()
	if !strings.Contains(body, `"status":"error"`) ||
		!strings.Contains(body, "章节在生成期间已被修改") {
		t.Fatalf("SSE body missing CAS conflict terminal: %s", body)
	}
}

func TestHandleGenerateChapterKeepsLeaseUntilSaveAndPublishComplete(t *testing.T) {
	saveEntered := make(chan struct{})
	saveRelease := make(chan struct{})
	publishEntered := make(chan struct{})
	publishRelease := make(chan struct{})
	published := make(chan *agents.GenerationState, 1)
	store := &generationChapterStoreFake{
		save: func(
			_ context.Context,
			target *generationChapterTarget,
			state *agents.GenerationState,
		) error {
			if target.ID != 11 || target.Title != "旧标题" ||
				state.ChapterID != "11" || state.Draft != validGeneratedContent() {
				return fmt.Errorf("unexpected save payload: target=%#v state=%#v", target, state)
			}
			close(saveEntered)
			<-saveRelease
			return nil
		},
	}
	engine := &generationTestEngine{
		run: func(_ context.Context, state *agents.GenerationState) (*agents.GenerationState, error) {
			state.Draft = validGeneratedContent()
			state.IsApproved = true
			return state, nil
		},
		publish: func(_ context.Context, state *agents.GenerationState) error {
			close(publishEntered)
			<-publishRelease
			published <- state
			return nil
		},
	}
	server := newServer(engine, nil)
	server.chapterStore = store
	firstDone := make(chan struct{})
	go func() {
		server.HandleGenerateChapter(
			httptest.NewRecorder(),
			generateRequestWithPersist(context.Background(), "7", 1, true),
		)
		close(firstDone)
	}()
	waitForSignal(t, saveEntered)

	select {
	case <-published:
		t.Fatal("chapter.generated published before save completed")
	default:
	}
	second := httptest.NewRecorder()
	server.HandleGenerateChapter(
		second,
		generateRequest(context.Background(), "7", 2),
	)
	if second.Code != http.StatusConflict {
		t.Fatalf("status during save = %d, want %d", second.Code, http.StatusConflict)
	}

	close(saveRelease)
	waitForSignal(t, publishEntered)
	second = httptest.NewRecorder()
	server.HandleGenerateChapter(
		second,
		generateRequest(context.Background(), "7", 2),
	)
	if second.Code != http.StatusConflict {
		t.Fatalf("status during publication = %d, want %d", second.Code, http.StatusConflict)
	}

	close(publishRelease)
	waitForSignal(t, firstDone)
	select {
	case state := <-published:
		if state.ChapterID != "11" || state.Draft != validGeneratedContent() {
			t.Fatalf("published state = %#v", state)
		}
	case <-time.After(time.Second):
		t.Fatal("chapter.generated was not published after save")
	}
}

func TestHandleGenerateChapterMemoryFailureKeepsSuccessAndReleasesLease(t *testing.T) {
	store := &generationChapterStoreFake{}
	engine := &generationTestEngine{
		run: func(_ context.Context, state *agents.GenerationState) (*agents.GenerationState, error) {
			state.Draft = validGeneratedContent()
			state.IsApproved = true
			return state, nil
		},
		publish: func(context.Context, *agents.GenerationState) error {
			return errors.New("memory update failed")
		},
	}
	server := newServer(engine, nil)
	server.chapterStore = store
	first := httptest.NewRecorder()

	server.HandleGenerateChapter(
		first,
		generateRequestWithPersist(context.Background(), "7", 1, true),
	)

	body := first.Body.String()
	if !strings.Contains(body, `"status":"success"`) ||
		strings.Contains(body, `"status":"error"`) {
		t.Fatalf("memory failure changed chapter terminal: %s", body)
	}

	second := httptest.NewRecorder()
	server.HandleGenerateChapter(
		second,
		generateRequest(context.Background(), "7", 2),
	)
	if second.Code == http.StatusConflict {
		t.Fatal("memory failure did not release novel lease")
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
