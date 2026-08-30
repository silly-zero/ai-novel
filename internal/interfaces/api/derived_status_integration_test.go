package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ai-novel/studio/ent"
	"github.com/ai-novel/studio/ent/chapter"
	"github.com/ai-novel/studio/ent/chapterderivedtask"
	"github.com/ai-novel/studio/ent/character"
	"github.com/ai-novel/studio/ent/characterstateversion"
	"github.com/ai-novel/studio/ent/novel"
	"github.com/ai-novel/studio/internal/application/usecases"
	"github.com/ai-novel/studio/internal/domain/agents"
	"github.com/ai-novel/studio/internal/domain/events"
	domain "github.com/ai-novel/studio/internal/domain/novel"
	databaseinfra "github.com/ai-novel/studio/internal/infrastructure/database"
	_ "github.com/lib/pq"
)

type switchEvidenceLLM struct {
	valid bool
}

func (l *switchEvidenceLLM) Generate(context.Context, string, string) (string, error) {
	evidence := "不存在"
	if l.valid {
		evidence = "文"
	}
	return fmt.Sprintf(`{"characters":[{"name":"文","current_status":"文","identity_evidence":"文","state_evidence":%q}],"relationships":[]}`, evidence), nil
}

func (*switchEvidenceLLM) StreamGenerate(context.Context, string, string, func(string) error) error {
	return nil
}

func TestPersistZeroUsesSavedNovelInputPostgres(t *testing.T) {
	dsn := os.Getenv("AI_NOVEL_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("AI_NOVEL_TEST_POSTGRES_DSN is not set")
	}
	client, err := ent.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	if err := client.Schema.Create(ctx); err != nil {
		t.Fatal(err)
	}
	novelRow, err := client.Novel.Create().SetTitle(fmt.Sprintf("persist-zero-%d", time.Now().UnixNano())).SetIdea("saved idea").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Novel.DeleteOneID(novelRow.ID).Exec(context.Background()) })
	seenIdea := ""
	engine := &generationTestEngine{
		prepare: func(_ context.Context, state *agents.GenerationState) (*agents.GenerationState, error) {
			seenIdea = state.Idea
			return state, nil
		},
		run: func(_ context.Context, state *agents.GenerationState) (*agents.GenerationState, error) {
			state.Draft = validGeneratedContent()
			state.IsApproved = true
			return state, nil
		},
	}
	server := newServer(engine, client)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/novel/generate", strings.NewReader(fmt.Sprintf(`{"novel_id":%d,"chapter_index":1,"persist":false}`, novelRow.ID)))
	request.Header.Set("Content-Type", "application/json")
	server.HandleGenerateChapter(recorder, request)
	if seenIdea != "saved idea" || !strings.Contains(recorder.Body.String(), `"status":"success"`) {
		t.Fatalf("idea=%q body=%s", seenIdea, recorder.Body.String())
	}
}

func TestChapterDetailReturnsCurrentDerivedTasksPostgres(t *testing.T) {
	dsn := os.Getenv("AI_NOVEL_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("AI_NOVEL_TEST_POSTGRES_DSN is not set")
	}
	client, err := ent.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	if err := client.Schema.Create(ctx); err != nil {
		t.Fatal(err)
	}
	novelRow, err := client.Novel.Create().SetTitle(fmt.Sprintf("derived-detail-%d", time.Now().UnixNano())).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	chapterRow, err := client.Chapter.Create().SetNovel(novelRow).SetTitle("第一章").SetContent("正文").SetWordCount(2).SetOrder(1).SetStatus(string(domain.StatusDraft)).SetDerivedStatus(string(domain.DerivedStatusFailed)).SetDerivedGenerationID("current-g").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	chapterID := chapterRow.ID
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = client.ChapterDerivedTask.Delete().Where(chapterderivedtask.ChapterID(chapterID)).Exec(cleanupCtx)
		_ = client.Chapter.DeleteOneID(chapterID).Exec(cleanupCtx)
		_ = client.Novel.DeleteOneID(novelRow.ID).Exec(cleanupCtx)
	})
	for _, generationID := range []string{"old-g", "current-g"} {
		for _, key := range domain.DerivedHandlerKeys {
			status := chapterderivedtask.StatusReady
			lastError := ""
			if generationID == "current-g" && key == domain.DerivedHandlerMemory {
				status = chapterderivedtask.StatusFailed
				lastError = "current failure"
			}
			if generationID == "old-g" {
				lastError = "old generation must not leak"
			}
			if _, err := client.ChapterDerivedTask.Create().SetChapterID(chapterID).SetGenerationID(generationID).SetHandlerKey(chapterderivedtask.HandlerKey(key)).SetStatus(status).SetAttempts(2).SetLastError(lastError).Save(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	server := newServer(&generationTestEngine{}, client)
	recorder := httptest.NewRecorder()
	server.router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/chapters/%d", chapterID), nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if strings.Contains(body, "old generation must not leak") || strings.Contains(body, "lease_token") || strings.Contains(body, "lease_until") {
		t.Fatalf("body leaked internal data: %s", body)
	}
	if !strings.Contains(body, `"derived_retryable":true`) || !strings.Contains(body, `"last_error":"current failure"`) || strings.Count(body, `"handler_key"`) != len(domain.DerivedHandlerKeys) {
		t.Fatalf("body = %s", body)
	}
}

func TestLedgerEvidenceFailureAndRetryUpdatesDerivedStatus(t *testing.T) {
	dsn := os.Getenv("AI_NOVEL_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("AI_NOVEL_TEST_POSTGRES_DSN is not set")
	}
	client, err := ent.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	if err := client.Schema.Create(ctx); err != nil {
		t.Fatal(err)
	}
	novelRow, err := client.Novel.Create().SetTitle(fmt.Sprintf("evidence-derived-%d", time.Now().UnixNano())).SetIdea("想法").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	chapterRow, err := client.Chapter.Create().SetNovel(novelRow).SetTitle("第一章").SetContent("旧正文").SetWordCount(3).SetOrder(1).SetStatus(string(domain.StatusDraft)).SetDerivedStatus(string(domain.DerivedStatusReady)).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	novelID := fmt.Sprintf("%d", novelRow.ID)
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		steps := []func() error{
			func() error {
				_, err := client.CharacterStateVersion.Delete().Where(characterstateversion.HasCharacterWith(character.NovelID(novelID))).Exec(cleanupCtx)
				return err
			},
			func() error {
				_, err := client.Character.Delete().Where(character.NovelID(novelID)).Exec(cleanupCtx)
				return err
			},
			func() error {
				_, err := client.Chapter.Delete().Where(chapter.HasNovelWith(novel.ID(novelRow.ID))).Exec(cleanupCtx)
				return err
			},
			func() error { return client.Novel.DeleteOneID(novelRow.ID).Exec(cleanupCtx) },
		}
		for _, step := range steps {
			if err := step(); err != nil {
				t.Errorf("cleanup evidence derived data: %v", err)
				return
			}
		}
	})
	llm := &switchEvidenceLLM{}
	repo := databaseinfra.NewCharacterRepository(client)
	characterUC := usecases.NewCharacterUseCase(agents.NewCharacterAgent(llm, repo))
	derived := usecases.NewDerivedOrchestrator(databaseinfra.NewDerivedTaskRepository(client), usecases.DerivedOrchestratorConfig{
		HandlerTimeout:    time.Minute,
		SettlementTimeout: time.Second,
	})
	for _, key := range domain.DerivedHandlerKeys {
		key := key
		handler := func(context.Context, events.ChapterGeneratedEvent) error { return nil }
		if key == domain.DerivedHandlerCharacter {
			handler = func(ctx context.Context, event events.ChapterGeneratedEvent) error {
				return characterUC.HandleChapterGenerated(ctx, event)
			}
		}
		if err := derived.Register(key, handler); err != nil {
			t.Fatal(err)
		}
	}
	engine := &generationTestEngine{
		run: func(_ context.Context, state *agents.GenerationState) (*agents.GenerationState, error) {
			state.Draft = validGeneratedContent()
			state.IsApproved = true
			return state, nil
		},
		publish: func(ctx context.Context, state *agents.GenerationState) error {
			return derived.RunCurrent(ctx, events.ChapterGeneratedEvent{
				GenerationID: state.GenerationID, NovelID: state.NovelID, ChapterID: state.ChapterID,
				ChapterIndex: state.ChapterIndex, Content: state.Draft, Timestamp: time.Now(),
			})
		},
	}
	server := newServer(engine, client)
	server.chapterStore = &entGenerationChapterStore{client: client}
	failed := httptest.NewRecorder()
	server.HandleGenerateChapter(failed, generateRequestWithPersist(context.Background(), novelID, 1, true))
	chapterRow, err = client.Chapter.Get(ctx, chapterRow.ID)
	if err != nil || chapterRow.DerivedStatus != string(domain.DerivedStatusFailed) {
		t.Fatalf("failed chapter=%#v error=%v body=%s", chapterRow, err, failed.Body.String())
	}
	if count, err := client.CharacterStateVersion.Query().Where(
		characterstateversion.HasCharacterWith(character.NovelID(novelID)),
	).Count(ctx); err != nil || count != 0 {
		t.Fatalf("character states after bad evidence=%d error=%v", count, err)
	}
	llm.valid = true
	retry := httptest.NewRecorder()
	server.router.ServeHTTP(retry, httptest.NewRequest("POST", fmt.Sprintf("/api/v1/chapters/%d/derived/retry", chapterRow.ID), nil))
	chapterRow, err = client.Chapter.Get(ctx, chapterRow.ID)
	if retry.Code != 200 || err != nil || chapterRow.DerivedStatus != string(domain.DerivedStatusReady) {
		t.Fatalf("retry=%d chapter=%#v error=%v body=%s", retry.Code, chapterRow, err, retry.Body.String())
	}
	if count, err := client.CharacterStateVersion.Query().Where(
		characterstateversion.HasCharacterWith(character.NovelID(novelID)),
	).Count(ctx); err != nil || count != 1 {
		t.Fatalf("character states after retry=%d error=%v", count, err)
	}
}

func TestGenerationPrepareRequiresPreviousDerivedReady(t *testing.T) {
	dsn := os.Getenv("AI_NOVEL_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("AI_NOVEL_TEST_POSTGRES_DSN is not set")
	}
	client, err := ent.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	if err := client.Schema.Create(ctx); err != nil {
		t.Fatal(err)
	}
	novelRow, chapters := createStaleTestNovel(t, ctx, client, "derived-gate", 1, 2)
	store := &entGenerationChapterStore{client: client}
	for _, status := range []domain.DerivedStatus{domain.DerivedStatusPending, domain.DerivedStatusFailed} {
		if _, err := client.Chapter.UpdateOneID(chapters[1].ID).SetDerivedStatus(string(status)).Save(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Prepare(ctx, novelRow.ID, chapters[2].ID, 2); err != nil {
			t.Fatalf("status %s prepare error = %v", status, err)
		}
	}
	if _, err := client.Chapter.UpdateOneID(chapters[1].ID).
		SetDerivedStatus(string(domain.DerivedStatusReady)).
		SetLastBeat("接力").SetOpenLoops([]string{}).SetNextAction("继续").Save(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Prepare(ctx, novelRow.ID, chapters[2].ID, 2); err != nil {
		t.Fatal(err)
	}
}

func TestGenerationDerivedStatusLifecycle(t *testing.T) {
	dsn := os.Getenv("AI_NOVEL_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("AI_NOVEL_TEST_POSTGRES_DSN is not set")
	}
	client, err := ent.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	if err := client.Schema.Create(ctx); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name         string
		publishErr   error
		wantDerived  domain.DerivedStatus
		wantTerminal string
	}{
		{name: "ready", wantDerived: domain.DerivedStatusReady, wantTerminal: `"status":"success"`},
		{name: "failed", publishErr: errors.New("derived unavailable"), wantDerived: domain.DerivedStatusFailed, wantTerminal: `"status":"error"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			novelRow, err := client.Novel.Create().SetTitle(fmt.Sprintf("derived-%s-%d", test.name, time.Now().UnixNano())).SetIdea("想法").Save(ctx)
			if err != nil {
				t.Fatal(err)
			}
			chapterRow, err := client.Chapter.Create().SetNovel(novelRow).SetTitle("第一章").SetContent("旧正文").SetWordCount(3).SetOrder(1).SetStatus(string(domain.StatusDraft)).SetDerivedStatus(string(domain.DerivedStatusReady)).Save(ctx)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				cleanupCtx := context.Background()
				if _, err := client.Chapter.Delete().Where(chapter.HasNovelWith(novel.ID(novelRow.ID))).Exec(cleanupCtx); err != nil {
					t.Errorf("cleanup derived chapters: %v", err)
					return
				}
				if err := client.Novel.DeleteOneID(novelRow.ID).Exec(cleanupCtx); err != nil {
					t.Errorf("cleanup derived novel: %v", err)
				}
			})
			currentPublishErr := test.publishErr
			derived := usecases.NewDerivedOrchestrator(databaseinfra.NewDerivedTaskRepository(client), usecases.DerivedOrchestratorConfig{
				HandlerTimeout:    time.Minute,
				SettlementTimeout: time.Second,
			})
			for _, key := range domain.DerivedHandlerKeys {
				key := key
				if err := derived.Register(key, func(context.Context, events.ChapterGeneratedEvent) error {
					if key == domain.DerivedHandlerMemory {
						return currentPublishErr
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			engine := &generationTestEngine{
				run: func(_ context.Context, state *agents.GenerationState) (*agents.GenerationState, error) {
					state.Draft = validGeneratedContent()
					state.IsApproved = true
					return state, nil
				},
				publish: func(ctx context.Context, state *agents.GenerationState) error {
					return derived.RunCurrent(ctx, events.ChapterGeneratedEvent{
						GenerationID: state.GenerationID, NovelID: state.NovelID, ChapterID: state.ChapterID,
						ChapterIndex: state.ChapterIndex, Content: state.Draft, Timestamp: time.Now(),
					})
				},
			}
			server := newServer(engine, client)
			server.chapterStore = &entGenerationChapterStore{client: client}
			recorder := httptest.NewRecorder()
			server.HandleGenerateChapter(
				recorder,
				generateRequestWithPersist(context.Background(), fmt.Sprintf("%d", novelRow.ID), 1, true),
			)
			if !strings.Contains(recorder.Body.String(), test.wantTerminal) {
				t.Fatalf("generation body = %s", recorder.Body.String())
			}
			chapterRow, err = client.Chapter.Get(ctx, chapterRow.ID)
			if err != nil || chapterRow.DerivedStatus != string(test.wantDerived) || chapterRow.DerivedGenerationID == "" {
				t.Fatalf("chapter = %#v, error=%v", chapterRow, err)
			}
			if test.wantDerived == domain.DerivedStatusFailed {
				currentPublishErr = nil
				retryServer := newServer(engine, client)
				retry := httptest.NewRecorder()
				retryServer.router.ServeHTTP(
					retry,
					httptest.NewRequest("POST", fmt.Sprintf("/api/v1/chapters/%d/derived/retry", chapterRow.ID), nil),
				)
				if retry.Code != 200 || !strings.Contains(retry.Body.String(), `"derived_status":"Ready"`) {
					t.Fatalf("retry response = %d %s", retry.Code, retry.Body.String())
				}
				chapterRow, err = client.Chapter.Get(ctx, chapterRow.ID)
				if err != nil || chapterRow.DerivedStatus != string(domain.DerivedStatusReady) {
					t.Fatalf("retried chapter = %#v, error=%v", chapterRow, err)
				}
				get := httptest.NewRecorder()
				retryServer.router.ServeHTTP(get, httptest.NewRequest("GET", fmt.Sprintf("/api/v1/chapters/%d", chapterRow.ID), nil))
				if get.Code != 200 || !strings.Contains(get.Body.String(), `"derived_status":"Ready"`) {
					t.Fatalf("chapter response = %d %s", get.Code, get.Body.String())
				}
				if _, err := client.Chapter.UpdateOneID(chapterRow.ID).SetDerivedStatus(string(domain.DerivedStatusFailed)).Save(ctx); err != nil {
					t.Fatal(err)
				}
				if _, err := client.ChapterDerivedTask.Update().Where(
					chapterderivedtask.ChapterID(chapterRow.ID),
					chapterderivedtask.GenerationID(chapterRow.DerivedGenerationID),
					chapterderivedtask.HandlerKeyEQ(chapterderivedtask.HandlerKey(domain.DerivedHandlerMemory)),
				).SetStatus(chapterderivedtask.StatusFailed).Save(ctx); err != nil {
					t.Fatal(err)
				}
				currentPublishErr = errors.New("still unavailable")
				failedRetryServer := newServer(engine, client)
				failedRetry := httptest.NewRecorder()
				failedRetryServer.router.ServeHTTP(
					failedRetry,
					httptest.NewRequest("POST", fmt.Sprintf("/api/v1/chapters/%d/derived/retry", chapterRow.ID), nil),
				)
				chapterRow, err = client.Chapter.Get(ctx, chapterRow.ID)
				if failedRetry.Code != 500 || err != nil || chapterRow.DerivedStatus != string(domain.DerivedStatusFailed) {
					t.Fatalf("failed retry = %d chapter=%#v error=%v body=%s", failedRetry.Code, chapterRow, err, failedRetry.Body.String())
				}
				if contentType := failedRetry.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
					t.Fatalf("failed retry content type = %q", contentType)
				}
				if body := failedRetry.Body.String(); !strings.Contains(body, `"derived_status":"Failed"`) || !strings.Contains(body, `"derived_tasks"`) || !strings.Contains(body, `"error"`) {
					t.Fatalf("failed retry body = %s", body)
				}
			}
		})
	}
}
