package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-novel/studio/ent"
	"github.com/ai-novel/studio/internal/application/workflows"
	"github.com/ai-novel/studio/internal/domain/agents"
	domain "github.com/ai-novel/studio/internal/domain/novel"
	llminfra "github.com/ai-novel/studio/internal/infrastructure/llm"
)

type generationDiagnosticCodeTestError struct {
	code  string
	cause error
}

func (e *generationDiagnosticCodeTestError) Error() string {
	return "safe diagnostic test error"
}

func (e *generationDiagnosticCodeTestError) Unwrap() error {
	return e.cause
}

func (e *generationDiagnosticCodeTestError) SafeDiagnosticCode() string {
	return e.code
}

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
	savedID      int
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
		ID:             11,
		Title:          "旧标题",
		Content:        "旧正文",
		WordCount:      3,
		Order:          chapterIndex,
		Status:         "Draft",
		UpdatedAt:      time.Unix(1, 0),
		NovelID:        7,
		NovelUpdatedAt: time.Unix(2, 0),
	}, nil
}

func (s *generationChapterStoreFake) Save(
	ctx context.Context,
	target *generationChapterTarget,
	state *agents.GenerationState,
) (int, error) {
	s.mu.Lock()
	s.saveCalls++
	s.mu.Unlock()
	if err := validateGenerationChapterSave(target, state); err != nil {
		return 0, err
	}
	if s.save != nil {
		if err := s.save(ctx, target, state); err != nil {
			return 0, err
		}
	}
	if s.savedID > 0 {
		return s.savedID, nil
	}
	return target.ID, nil
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
		LastBeat:   "文",
		NextAction: "文",
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

func TestChapterDerivedRetryable(t *testing.T) {
	tests := []struct {
		name string
		row  *ent.Chapter
		want bool
	}{
		{name: "failed", row: &ent.Chapter{Status: string(domain.StatusDraft), DerivedStatus: string(domain.DerivedStatusFailed), DerivedGenerationID: "g"}, want: true},
		{name: "pending", row: &ent.Chapter{Status: string(domain.StatusDraft), DerivedStatus: string(domain.DerivedStatusPending), DerivedGenerationID: "g"}, want: true},
		{name: "ready", row: &ent.Chapter{Status: string(domain.StatusDraft), DerivedStatus: string(domain.DerivedStatusReady), DerivedGenerationID: "g"}},
		{name: "stale", row: &ent.Chapter{Status: string(domain.StatusStale), DerivedStatus: string(domain.DerivedStatusFailed), DerivedGenerationID: "g"}},
		{name: "empty generation", row: &ent.Chapter{Status: string(domain.StatusDraft), DerivedStatus: string(domain.DerivedStatusFailed)}},
		{name: "nil"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := chapterDerivedRetryable(test.row); got != test.want {
				t.Fatalf("chapterDerivedRetryable() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestDerivedTaskItemsBoundErrors(t *testing.T) {
	longError := strings.Repeat("错", maxDerivedAPIErrorRunes+10)
	items := derivedTaskItems([]domain.DerivedTask{{
		HandlerKey: "memory",
		Status:     domain.DerivedTaskFailed,
		Attempts:   2,
		LastError:  "  generation_id=g-secret lease_token: l-secret task_id=42 " + longError + "  ",
	}})
	if len(items) != 1 || len([]rune(items[0].LastError)) != maxDerivedAPIErrorRunes {
		t.Fatalf("items = %#v", items)
	}
	if strings.Contains(items[0].LastError, "g-secret") || strings.Contains(items[0].LastError, "l-secret") || strings.Contains(items[0].LastError, "task_id") {
		t.Fatalf("last error leaked identifiers: %q", items[0].LastError)
	}
	if !strings.Contains(items[0].LastError, "错") {
		t.Fatalf("last error lost valid rune text: %q", items[0].LastError)
	}
}

func TestBoundedDerivedAPIErrorRedactsJSONIdentifiers(t *testing.T) {
	got := boundedDerivedAPIError(`{"generation_id":"g-secret","lease_token":"l-secret","task_id":"42"}`)
	for _, secret := range []string{"g-secret", "l-secret", `"42"`} {
		if strings.Contains(got, secret) {
			t.Fatalf("error leaked %q: %s", secret, got)
		}
	}
}

func TestWriteChapterDerivedSnapshot(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeChapterDerivedSnapshot(recorder, http.StatusInternalServerError, ChapterDerivedSnapshot{
		ChapterID:        "1",
		DerivedStatus:    string(domain.DerivedStatusFailed),
		DerivedRetryable: true,
		DerivedTasks:     []DerivedTaskItem{},
		Error:            "failed",
	})
	if recorder.Code != http.StatusInternalServerError || !strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("response = %d %#v", recorder.Code, recorder.Header())
	}
	if body := recorder.Body.String(); !strings.Contains(body, `"derived_tasks":[]`) || !strings.Contains(body, `"derived_retryable":true`) {
		t.Fatalf("body = %s", body)
	}
}

func generationTerminalFromSSE(t *testing.T, body string) generationResult {
	t.Helper()
	blocks := strings.Split(body, "\n\n")
	for _, block := range blocks {
		if !strings.HasPrefix(block, "event: terminal\n") {
			continue
		}
		data := strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(block, "event: terminal\n")), "data: ")
		var result generationResult
		if err := json.Unmarshal([]byte(data), &result); err != nil {
			t.Fatalf("decode terminal: %v; block=%s", err, block)
		}
		return result
	}
	t.Fatalf("terminal event not found: %s", body)
	return generationResult{}
}

func validGeneratedContent() string {
	return strings.Repeat("文", 2500)
}

func generateRequest(ctx context.Context, novelID string, chapterIndex int) *http.Request {
	return generateRequestWithPersist(ctx, novelID, chapterIndex, false)
}
func generateRequestWithPersist(ctx context.Context, novelID string, chapterIndex int, persist bool) *http.Request {
	body := fmt.Sprintf(`{"novel_id":%s,"chapter_index":%d,"persist":%t,"idea":"test"}`, novelID, chapterIndex, persist)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/novel/generate", strings.NewReader(body)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	return request
}

func generateJSONRequest(body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/novel/generate", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	return request
}
func previewJSONRequest(body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/novel/preview-context", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	return request
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

func TestGenerateAndPreviewRejectExplicitNull(t *testing.T) {
	calls := 0
	engine := &generationTestEngine{prepare: func(_ context.Context, state *agents.GenerationState) (*agents.GenerationState, error) {
		calls++
		return state, nil
	}}
	server := newServer(engine, nil)
	tests := []struct {
		name    string
		handler func(http.ResponseWriter, *http.Request)
		request func() *http.Request
		field   string
	}{}
	for _, field := range []string{"novel_id", "chapter_id", "persist", "chapter_index", "outline", "idea", "existing_outline", "outline_start", "outline_end", "editor_notes", "manual_context"} {
		field := field
		tests = append(tests, struct {
			name    string
			handler func(http.ResponseWriter, *http.Request)
			request func() *http.Request
			field   string
		}{"generate " + field, server.HandleGenerateChapter, func() *http.Request { return generateJSONRequest(fmt.Sprintf(`{"novel_id":7,"%s":null}`, field)) }, field})
	}
	for _, field := range []string{"novel_id", "chapter_index", "outline", "idea", "existing_outline", "outline_start", "outline_end", "editor_notes", "manual_context"} {
		field := field
		tests = append(tests, struct {
			name    string
			handler func(http.ResponseWriter, *http.Request)
			request func() *http.Request
			field   string
		}{"preview " + field, server.HandlePreviewContext, func() *http.Request { return previewJSONRequest(fmt.Sprintf(`{"novel_id":7,"%s":null}`, field)) }, field})
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			test.handler(recorder, test.request())
			if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), test.field+" must not be null") {
				t.Fatalf("response=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
	if calls != 0 {
		t.Fatalf("engine calls = %d", calls)
	}
}
func TestGenerateAndPreviewRejectInvalidChapterIndex(t *testing.T) {
	engine := &generationTestEngine{}
	server := newServer(engine, nil)
	tests := []struct {
		name    string
		handler func(http.ResponseWriter, *http.Request)
		request func() *http.Request
	}{
		{
			name:    "generate zero",
			handler: server.HandleGenerateChapter,
			request: func() *http.Request {
				return generateJSONRequest(`{"novel_id":7,"idea":"test","persist":false,"chapter_index":0}`)
			},
		},
		{
			name:    "generate malformed",
			handler: server.HandleGenerateChapter,
			request: func() *http.Request {
				return generateJSONRequest(`{"novel_id":7,"idea":"test","persist":false,"chapter_index":"bad"}`)
			},
		},
		{
			name:    "preview negative",
			handler: server.HandlePreviewContext,
			request: func() *http.Request { return previewJSONRequest(`{"novel_id":7,"idea":"test","chapter_index":-1}`) },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			test.handler(recorder, test.request())
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("response = %d, body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestContextMetaContainsOnlySafeSummary(t *testing.T) {
	engine := &generationTestEngine{run: func(_ context.Context, state *agents.GenerationState) (*agents.GenerationState, error) {
		state.Draft = validGeneratedContent()
		state.IsApproved = true
		return state, nil
	}}
	server := newServer(engine, nil)
	recorder := httptest.NewRecorder()
	server.HandleGenerateChapter(recorder, generateRequest(context.Background(), "7", 1))
	body := recorder.Body.String()
	metaStart := strings.Index(body, "event: context_meta\n")
	metaEnd := strings.Index(body[metaStart:], "\n\nevent:")
	if metaStart < 0 || metaEnd < 0 {
		t.Fatalf("context meta missing: %s", body)
	}
	meta := body[metaStart : metaStart+metaEnd]
	if !strings.Contains(meta, `"context_stats"`) || strings.Contains(meta, "editor_notes") || strings.Contains(meta, "manual_context") || strings.Contains(meta, "context_preview") || strings.Contains(meta, "scene_card_preview") || strings.Contains(meta, "full_outline_preview") || strings.Contains(meta, "generation_id") {
		t.Fatalf("unsafe context meta: %s", meta)
	}
}

func TestPreviewContextRouteUsesPostJSON(t *testing.T) {
	engine := &generationTestEngine{prepare: func(_ context.Context, state *agents.GenerationState) (*agents.GenerationState, error) {
		state.FullOutline = "完整大纲"
		state.Outline = "章节大纲"
		return state, nil
	}}
	server := newServer(engine, nil)
	get := httptest.NewRecorder()
	server.router.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/v1/novel/preview-context?novel_id=7&idea=secret", nil))
	if get.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d", get.Code)
	}
	post := httptest.NewRecorder()
	server.router.ServeHTTP(post, previewJSONRequest(`{"novel_id":7,"chapter_index":2,"idea":" 想法 ","editor_notes":" 备注 "}`))
	if post.Code != http.StatusOK || !strings.Contains(post.Body.String(), `"novel_id":"7"`) || !strings.Contains(post.Body.String(), `"chapter_index":2`) {
		t.Fatalf("POST response = %d %s", post.Code, post.Body.String())
	}
	if post.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q", post.Header().Get("Cache-Control"))
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
		request := previewJSONRequest(`{"novel_id":7,"idea":"test"}`)
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

func TestGenerationDiagnosticLogContainsOnlySafeMetadata(t *testing.T) {
	oldWriter, oldFlags := log.Writer(), log.Flags()
	var output bytes.Buffer
	log.SetOutput(&output)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
	})

	logGenerationDiagnostic(
		"generation-log-test",
		"chapter_generation",
		"error",
		"provider_busy",
		workflows.NewWorkflowStageError(
			workflows.WorkflowStageArchitect,
			&generationDiagnosticCodeTestError{
				code:  "generated_outline_missing_chapter",
				cause: &llminfra.ProviderError{StatusCode: 429, Retryable: true},
			},
		),
	)
	got := output.String()
	for _, want := range []string{
		"generation_id=generation-log-test",
		"stage=chapter_generation",
		"status=error",
		"error_code=provider_busy",
		"provider_status=429",
		"workflow_stage=architect",
		"issue_code=generated_outline_missing_chapter",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("log missing %q: %s", want, got)
		}
	}
	for _, secret := range []string{
		"CANARY_DRAFT",
		"response preview",
		"prompt=",
		"evidence=",
	} {
		if strings.Contains(got, secret) {
			t.Fatalf("log leaked %q: %s", secret, got)
		}
	}
}

func TestGenerationDiagnosticLogAllowsReviewerProtocolIssues(t *testing.T) {
	for _, issueCode := range []string{
		"empty_model_response",
		"structured_response_invalid",
		"reviewer_empty_draft",
		"reviewer_json_shape_type",
		"reviewer_required_field",
		"reviewer_array_structure",
		"reviewer_evidence_head",
		"reviewer_evidence_tail",
		"reviewer_evidence_draft_support",
		"reviewer_evidence_draft_violation",
		"reviewer_evidence_span",
		"reviewer_critique_missing",
		"reviewer_nullability",
		"reviewer_evidence_empty",
		"reviewer_evidence_too_long",
	} {
		t.Run(issueCode, func(t *testing.T) {
			oldWriter, oldFlags := log.Writer(), log.Flags()
			var output bytes.Buffer
			log.SetOutput(&output)
			log.SetFlags(0)
			t.Cleanup(func() {
				log.SetOutput(oldWriter)
				log.SetFlags(oldFlags)
			})

			logGenerationDiagnostic(
				"generation-log-test",
				"chapter_generation",
				"error",
				"review_protocol_error",
				workflows.NewWorkflowStageError(
					workflows.WorkflowStageReviewer,
					&generationDiagnosticCodeTestError{code: issueCode, cause: errors.New("CANARY_CAUSE")},
				),
			)
			got := output.String()
			for _, want := range []string{
				"error_code=review_protocol_error",
				"workflow_stage=reviewer",
				"issue_code=" + issueCode,
			} {
				if !strings.Contains(got, want) {
					t.Fatalf("log missing %q: %s", want, got)
				}
			}
			if strings.Contains(got, "CANARY_CAUSE") {
				t.Fatalf("log leaked cause: %s", got)
			}
		})
	}
}

func TestGenerationDiagnosticLogOmitsDeprecatedReviewerAggregates(t *testing.T) {
	for _, code := range []string{
		"reviewer_validation_other",
		"reviewer_evidence_draft",
	} {
		t.Run(code, func(t *testing.T) {
			oldWriter, oldFlags := log.Writer(), log.Flags()
			var output bytes.Buffer
			log.SetOutput(&output)
			log.SetFlags(0)
			t.Cleanup(func() {
				log.SetOutput(oldWriter)
				log.SetFlags(oldFlags)
			})

			logGenerationDiagnostic(
				"generation-log-test",
				"chapter_generation",
				"error",
				"review_protocol_error",
				workflows.NewWorkflowStageError(
					workflows.WorkflowStageReviewer,
					&generationDiagnosticCodeTestError{code: code},
				),
			)
			if got := output.String(); strings.Contains(got, "issue_code=") {
				t.Fatalf("deprecated aggregate leaked into log: %s", got)
			}
		})
	}
}

func TestGenerationDiagnosticLogOmitsUnknownWorkflowStage(t *testing.T) {
	oldWriter, oldFlags := log.Writer(), log.Flags()
	var output bytes.Buffer
	log.SetOutput(&output)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
	})

	logGenerationDiagnostic(
		"generation-log-test",
		"chapter_generation",
		"error",
		"generation_failed",
		workflows.NewWorkflowStageError(
			"CANARY_STAGE\nforged=true",
			&generationDiagnosticCodeTestError{code: "CANARY_ISSUE\nforged=true", cause: errors.New("CANARY_CAUSE")},
		),
	)
	got := output.String()
	if strings.Contains(got, "workflow_stage=") || strings.Contains(got, "issue_code=") || strings.Contains(got, "CANARY") {
		t.Fatalf("log leaked untrusted stage or cause: %s", got)
	}
}

func TestGenerationDiagnosticLogRejectsUntrustedFields(t *testing.T) {
	oldWriter, oldFlags := log.Writer(), log.Flags()
	var output bytes.Buffer
	log.SetOutput(&output)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
	})

	logGenerationDiagnostic(
		"CANARY_PROMPT\nforged=true",
		"CANARY_DRAFT",
		"CANARY_RESPONSE",
		"CANARY_EVIDENCE",
		workflows.NewWorkflowStageError("CANARY_STAGE", errors.New("CANARY_CAUSE")),
	)
	got := output.String()
	for _, want := range []string{
		"generation_id=invalid",
		"stage=admission",
		"status=error",
		"error_code=generation_failed",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("log missing %q: %s", want, got)
		}
	}
	for _, secret := range []string{
		"CANARY_PROMPT",
		"CANARY_DRAFT",
		"CANARY_RESPONSE",
		"CANARY_EVIDENCE",
		"provider_status=999",
		"forged=true",
	} {
		if strings.Contains(got, secret) {
			t.Fatalf("log leaked %q: %s", secret, got)
		}
	}
}

func TestHandleGenerateChapterContextFailureDoesNotLeakDiagnostics(t *testing.T) {
	const sensitiveError = "CANARY_DRAFT CANARY_PROMPT CANARY_RESPONSE CANARY_EVIDENCE"
	engine := &generationTestEngine{
		prepare: func(context.Context, *agents.GenerationState) (*agents.GenerationState, error) {
			return nil, errors.New(sensitiveError)
		},
	}
	server := newServer(engine, nil)
	oldWriter, oldFlags := log.Writer(), log.Flags()
	var output bytes.Buffer
	log.SetOutput(&output)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
	})
	recorder := httptest.NewRecorder()

	server.HandleGenerateChapter(
		recorder,
		generateRequest(context.Background(), "7", 1),
	)

	terminal := generationTerminalFromSSE(t, recorder.Body.String())
	if terminal.Status != generationStatusError ||
		terminal.ErrorCode != "context_preparation_failed" {
		t.Fatalf("terminal = %#v", terminal)
	}
	got := output.String()
	for _, want := range []string{
		"generation_id=" + terminal.GenerationID,
		"stage=context_preparation",
		"status=error",
		"error_code=context_preparation_failed",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("log missing %q: %s", want, got)
		}
	}
	for _, secret := range strings.Fields(sensitiveError) {
		if strings.Contains(got, secret) ||
			strings.Contains(recorder.Body.String(), secret) {
			t.Fatalf("diagnostics leaked %q: log=%s body=%s", secret, got, recorder.Body.String())
		}
	}
}

func TestHandleGenerateChapterSuccessDoesNotLogGeneratedContent(t *testing.T) {
	const sensitiveDraft = "CANARY_DRAFT CANARY_RESPONSE CANARY_EVIDENCE"
	engine := &generationTestEngine{
		run: func(_ context.Context, state *agents.GenerationState) (*agents.GenerationState, error) {
			state.Draft = sensitiveDraft
			state.IsApproved = true
			return state, nil
		},
	}
	server := newServer(engine, nil)
	oldWriter, oldFlags := log.Writer(), log.Flags()
	var output bytes.Buffer
	log.SetOutput(&output)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
	})
	recorder := httptest.NewRecorder()

	server.HandleGenerateChapter(
		recorder,
		generateRequest(context.Background(), "7", 1),
	)

	terminal := generationTerminalFromSSE(t, recorder.Body.String())
	if terminal.Status != generationStatusSuccess {
		t.Fatalf("terminal = %#v", terminal)
	}
	got := output.String()
	for _, want := range []string{
		"generation_id=" + terminal.GenerationID,
		"stage=context_preparation status=started",
		"stage=terminal_delivery status=success",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("log missing %q: %s", want, got)
		}
	}
	for _, secret := range strings.Fields(sensitiveDraft) {
		if strings.Contains(got, secret) {
			t.Fatalf("success log leaked %q: %s", secret, got)
		}
	}
}

func TestPublicGenerationErrorClassification(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code string
	}{
		{"retryable provider", &llminfra.ProviderError{Operation: "chat", StatusCode: 429, Retryable: true}, "provider_busy"},
		{"permanent provider", &llminfra.ProviderError{Operation: "chat", StatusCode: 401}, "provider_error"},
		{"context marker", &contextPreparationError{cause: errors.New("CANARY_CONTEXT")}, "context_preparation_failed"},
		{"architect stage", workflows.NewWorkflowStageError(workflows.WorkflowStageArchitect, errors.New("CANARY_ARCHITECT")), "context_preparation_failed"},
		{"plot stage", workflows.NewWorkflowStageError(workflows.WorkflowStagePlot, errors.New("CANARY_PLOT")), "context_preparation_failed"},
		{"director stage", workflows.NewWorkflowStageError(workflows.WorkflowStageDirector, errors.New("CANARY_DIRECTOR")), "context_preparation_failed"},
		{"librarian stage", workflows.NewWorkflowStageError(workflows.WorkflowStageLibrarian, errors.New("CANARY_LIBRARIAN")), "context_preparation_failed"},
		{"writer stage", workflows.NewWorkflowStageError(workflows.WorkflowStageWriter, errors.New("CANARY_WRITER")), "generation_failed"},
		{"reviewer stage", workflows.NewWorkflowStageError(workflows.WorkflowStageReviewer, errors.New("CANARY_REVIEWER")), "review_protocol_error"},
		{"review retry limit", workflows.NewWorkflowStageError(workflows.WorkflowStageReviewer, workflows.ErrReviewRetryLimit), "review_failed"},
		{"old context text", errors.New("context preparation failed: CANARY_CONTEXT"), "generation_failed"},
		{"old reviewer text", errors.New("reviewer agent failed: CANARY_REVIEWER"), "generation_failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			code, _ := publicGenerationError(test.err)
			if code != test.code {
				t.Fatalf("code=%s want=%s", code, test.code)
			}
		})
	}
}

func TestPublicGenerationErrorClassificationPreservesPrecedence(t *testing.T) {
	providerErr := &llminfra.ProviderError{Operation: "chat", StatusCode: 429, Retryable: true}
	stageErr := workflows.NewWorkflowStageError(workflows.WorkflowStageReviewer, providerErr)
	code, _ := publicGenerationError(stageErr)
	if code != "provider_busy" {
		t.Fatalf("provider code=%s, want provider_busy", code)
	}

	cancelErr := workflows.NewWorkflowStageError(workflows.WorkflowStageWriter, context.Canceled)
	code, _ = publicGenerationError(cancelErr)
	if code != "generation_cancelled" {
		t.Fatalf("cancel code=%s, want generation_cancelled", code)
	}
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
		!strings.Contains(recorder.Body.String(), `"error_code":"generation_timeout"`) {
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
		!strings.Contains(recorder.Body.String(), `"error_code":"generation_timeout"`) {
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
	server.HandleGenerateChapter(second, generateRequest(context.Background(), "7", 2))
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
	if !strings.Contains(first.Body.String(), `"error_code":"context_preparation_failed"`) ||
		!strings.Contains(first.Body.String(), `"message":"上下文准备失败，请重试"`) ||
		strings.Contains(first.Body.String(), "prepare failed") {
		t.Fatalf("first response = %q, want sanitized prepare error", first.Body.String())
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

func TestPreparePreviousContinuityRejectsStalePreviousChapter(t *testing.T) {
	packet, err := preparePreviousContinuity(
		context.Background(),
		7,
		3,
		func(context.Context, int, int) (*ent.Chapter, error) {
			return &ent.Chapter{Status: string(domain.StatusStale), LastBeat: "旧", NextAction: "旧"}, nil
		},
	)
	if !packet.IsEmpty() || !errors.Is(err, errGenerationEarlierChapterStale) {
		t.Fatalf("packet = %#v, error = %v", packet, err)
	}
}

func TestPreparePreviousContinuityRejectsDerivedNotReady(t *testing.T) {
	for _, status := range []domain.DerivedStatus{domain.DerivedStatusPending, domain.DerivedStatusFailed} {
		t.Run(string(status), func(t *testing.T) {
			packet, err := preparePreviousContinuity(
				context.Background(), 7, 3,
				func(context.Context, int, int) (*ent.Chapter, error) {
					return &ent.Chapter{DerivedStatus: string(status), LastBeat: "旧", NextAction: "旧"}, nil
				},
			)
			if !packet.IsEmpty() || !errors.Is(err, errGenerationPreviousDerivedNotReady) {
				t.Fatalf("packet=%#v error=%v", packet, err)
			}
		})
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
				DerivedStatus: string(domain.DerivedStatusReady),
				LastBeat:      "  最后动作  ",
				OpenLoops:     loops,
				NextAction:    "  继续追查  ",
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
			return &ent.Chapter{DerivedStatus: string(domain.DerivedStatusReady)}, nil
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

func TestPrepareNewGenerationChapterRejectsMissingPredecessor(t *testing.T) {
	target, err := prepareNewGenerationChapter(
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
	)
	if !errors.Is(err, errGenerationPreviousChapterMissing) || target != nil {
		t.Fatalf("target = %#v, error = %v", target, err)
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
			return &ent.Chapter{DerivedStatus: string(domain.DerivedStatusReady), LastBeat: "结尾", NextAction: "行动"}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(events, ",") != "lock,target,lookup" || target.ID != 0 || !target.isNew || target.Order != 3 || target.PreviousContinuity.LastBeat != "结尾" {
		t.Fatalf("events = %v, target = %#v", events, target)
	}
}

func TestPrepareNewGenerationChapterReusesTargetAfterLock(t *testing.T) {
	target, err := prepareNewGenerationChapter(
		context.Background(),
		7,
		3,
		func(context.Context, int) error { return nil },
		func(context.Context, int, int) (*ent.Chapter, error) {
			return &ent.Chapter{ID: 11, Order: 3}, nil
		},
		func(context.Context, int, int) (*ent.Chapter, error) {
			return &ent.Chapter{DerivedStatus: string(domain.DerivedStatusReady), LastBeat: "结尾", NextAction: "行动"}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if target.ID != 11 || target.isNew || target.PreviousContinuity.LastBeat != "结尾" {
		t.Fatalf("target = %#v", target)
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

func TestChapterSuccessorResult(t *testing.T) {
	if err := chapterSuccessorResult(false); err != nil {
		t.Fatalf("no successor error = %v", err)
	}
	if err := chapterSuccessorResult(true); !errors.Is(err, errChapterHasSuccessor) {
		t.Fatalf("successor error = %v", err)
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
			_, err := store.Save(context.Background(), test.target, test.state)
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
			Draft:      "结尾动作。下一步动作。" + strings.Repeat("文", 2500-len([]rune("结尾动作。下一步动作。"))),
			IsApproved: true,
			Continuity: agents.ContinuityPacket{
				LastBeat:   "结尾动作。",
				NextAction: "下一步动作。",
			},
		},
	)
	if err != nil {
		t.Fatalf("validateGenerationChapterSave() error = %v", err)
	}
}

func TestValidateGenerationChapterSaveRejectsUnsupportedContinuityEvidence(t *testing.T) {
	err := validateGenerationChapterSave(
		&generationChapterTarget{ID: 11},
		&agents.GenerationState{
			Draft:      strings.Repeat("文", 2500),
			IsApproved: true,
			Continuity: agents.ContinuityPacket{
				LastBeat:   "正文不存在的结尾",
				NextAction: "正文不存在的动作",
			},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "last_beat must be an exact draft substring") {
		t.Fatalf("error = %v", err)
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

func TestValidateGenerationNovelSource(t *testing.T) {
	revision := time.Unix(2, 0)
	if err := validateGenerationNovelSource(revision, revision); err != nil {
		t.Fatalf("same revision error = %v", err)
	}
	if err := validateGenerationNovelSource(revision, time.Unix(3, 0)); !errors.Is(err, errGenerationChapterChanged) {
		t.Fatalf("changed revision error = %v", err)
	}
	if err := validateGenerationNovelSource(time.Time{}, revision); !errors.Is(err, errGenerationChapterChanged) {
		t.Fatalf("missing revision error = %v", err)
	}
}

func TestGenerationNovelSourceMatches(t *testing.T) {
	revision := time.Unix(2, 0)
	for _, test := range []struct {
		name     string
		expected time.Time
		actual   time.Time
		want     bool
	}{
		{name: "same revision", expected: revision, actual: revision, want: true},
		{name: "changed revision", expected: revision, actual: time.Unix(3, 0)},
		{name: "missing expected revision", actual: revision},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := generationNovelSourceMatches(test.expected, test.actual); got != test.want {
				t.Fatalf("generationNovelSourceMatches() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestHandleGenerateChapterPassesNovelSourceRevisionToSave(t *testing.T) {
	wantNovelID := 7
	wantRevision := time.Unix(2, 0)
	var savedTarget *generationChapterTarget
	store := &generationChapterStoreFake{
		prepare: func(context.Context, int, int, int) (*generationChapterTarget, error) {
			return &generationChapterTarget{
				ID:             11,
				Order:          1,
				Status:         "Draft",
				UpdatedAt:      time.Unix(1, 0),
				NovelID:        wantNovelID,
				NovelUpdatedAt: wantRevision,
			}, nil
		},
		save: func(_ context.Context, target *generationChapterTarget, _ *agents.GenerationState) error {
			savedTarget = target
			return nil
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
	server.HandleGenerateChapter(
		httptest.NewRecorder(),
		generateRequestWithPersist(context.Background(), "7", 1, true),
	)
	if savedTarget == nil || savedTarget.NovelID != wantNovelID || !savedTarget.NovelUpdatedAt.Equal(wantRevision) {
		t.Fatalf("saved target = %#v", savedTarget)
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
	if !strings.Contains(body, `"error_code":"generation_failed"`) || strings.Contains(body, "database not configured") {
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
	if !strings.Contains(body, `"error_code":"generation_failed"`) || strings.Contains(body, "chapter 2") || strings.Contains(body, "novel 7") {
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
	if strings.Contains(recorder.Body.String(), `"persisted":true`) {
		t.Fatalf("persist=0 terminal claims persisted chapter: %s", recorder.Body.String())
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
	request := generateJSONRequest(`{"novel_id":7,"chapter_id":11,"idea":"test","persist":true}`)

	server.HandleGenerateChapter(recorder, request)

	if generatedIndex != 4 {
		t.Fatalf("chapter index = %d, want persisted order 4", generatedIndex)
	}
	terminal := generationTerminalFromSSE(t, recorder.Body.String())
	if terminal.Status != generationStatusSuccess || terminal.ChapterID != "11" || !terminal.Persisted {
		t.Fatalf("terminal = %#v; body=%s", terminal, recorder.Body.String())
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
	if !strings.Contains(body, `"error_code":"generation_failed"`) || strings.Contains(body, "prompt_label_leak") {
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
	if !strings.Contains(body, `"error_code":"generation_failed"`) || strings.Contains(body, "database unavailable") {
		t.Fatalf("SSE body missing save error terminal: %s", body)
	}
	if strings.Contains(body, `"persisted":true`) {
		t.Fatalf("save failure terminal claims persisted chapter: %s", body)
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
		if state.ChapterID != "11" || state.ChapterIndex != 1 || state.Draft != validGeneratedContent() {
			t.Fatalf("published state = %#v", state)
		}
	case <-time.After(time.Second):
		t.Fatal("chapter.generated was not published after save")
	}
}

func TestHandleGenerateChapterDerivedFailureUsesErrorAndReleasesLease(t *testing.T) {
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
	terminal := generationTerminalFromSSE(t, body)
	if terminal.Status != generationStatusError || terminal.ChapterID != "11" || !terminal.Persisted || terminal.ErrorCode != "derived_processing_failed" {
		t.Fatalf("derived failure terminal=%#v body=%s", terminal, body)
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
	if !strings.Contains(body, `"error_code":"generation_failed"`) || strings.Contains(body, `provider failed`) {
		t.Fatalf("SSE body missing JSON error terminal: %s", body)
	}
	if strings.Contains(body, "event: end") || strings.Contains(body, "event: error") {
		t.Fatalf("SSE body contains legacy terminal: %s", body)
	}
}

func TestHandleGenerateChapterReviewerProtocolFailureIsSanitized(t *testing.T) {
	const canary = "CANARY_RESPONSE CANARY_EVIDENCE CANARY_PROMPT CANARY_DRAFT"
	engine := &generationTestEngine{run: func(
		context.Context,
		*agents.GenerationState,
	) (*agents.GenerationState, error) {
		return nil, workflows.NewWorkflowStageError(
			workflows.WorkflowStageReviewer,
			&generationDiagnosticCodeTestError{
				code:  "reviewer_evidence_tail",
				cause: errors.New(canary),
			},
		)
	}}
	server := newServer(engine, nil)
	oldWriter, oldFlags := log.Writer(), log.Flags()
	var output bytes.Buffer
	log.SetOutput(&output)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
	})
	recorder := httptest.NewRecorder()

	server.HandleGenerateChapter(
		recorder,
		generateRequest(context.Background(), "7", 1),
	)

	body := recorder.Body.String()
	if count := strings.Count(body, "event: terminal"); count != 1 {
		t.Fatalf("terminal count = %d, want 1; body: %s", count, body)
	}
	if !strings.Contains(body, `"error_code":"review_protocol_error"`) ||
		!strings.Contains(body, `"message":"审查响应异常，请稍后重试"`) ||
		strings.Contains(body, "workflow_stage") || strings.Contains(body, "issue_code") {
		t.Fatalf("terminal = %s", body)
	}
	gotLog := output.String()
	for _, want := range []string{
		"error_code=review_protocol_error",
		"workflow_stage=reviewer",
		"issue_code=reviewer_evidence_tail",
	} {
		if !strings.Contains(gotLog, want) {
			t.Fatalf("log missing %q: %s", want, gotLog)
		}
	}
	for _, secret := range strings.Fields(canary) {
		if strings.Contains(body, secret) || strings.Contains(gotLog, secret) {
			t.Fatalf("review protocol failure leaked %q", secret)
		}
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
	if !strings.Contains(body, `"error_code":"context_preparation_failed"`) ||
		!strings.Contains(body, `"message":"上下文准备失败，请重试"`) ||
		strings.Contains(body, `prepare failed`) {
		t.Fatalf("SSE body missing sanitized prepare error terminal: %s", body)
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
	if !strings.Contains(body, `"error_code":"generation_failed"`) || strings.Contains(body, "generation returned no final state") {
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
	if !strings.Contains(body, `"error_code":"generation_protocol_error"`) || strings.Contains(body, errGenerationProtocol.Error()) {
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

func TestGenerationGuardMutationAndGenerationAreMutuallyExclusive(t *testing.T) {
	guard := newGenerationGuard()
	if !guard.acquireMutation(7) {
		t.Fatal("first mutation lease was rejected")
	}
	if guard.acquire(7, "generation", context.Background(), func(error) {}) {
		t.Fatal("generation acquired during mutation")
	}
	if guard.acquireMutation(7) {
		t.Fatal("second mutation acquired concurrently")
	}
	guard.releaseMutation(7)
	if !guard.acquire(7, "generation", context.Background(), func(error) {}) {
		t.Fatal("generation lease was rejected after mutation release")
	}
	if guard.acquireMutation(7) {
		t.Fatal("mutation acquired during generation")
	}
	guard.release(7, "generation")
	if !guard.acquireMutation(7) {
		t.Fatal("mutation lease was rejected after generation release")
	}
	guard.releaseMutation(7)
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
