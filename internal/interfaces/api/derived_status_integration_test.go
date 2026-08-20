package api

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ai-novel/studio/ent"
	"github.com/ai-novel/studio/ent/chapter"
	"github.com/ai-novel/studio/ent/novel"
	"github.com/ai-novel/studio/internal/domain/agents"
	domain "github.com/ai-novel/studio/internal/domain/novel"
	_ "github.com/lib/pq"
)

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
		if _, err := store.Prepare(ctx, novelRow.ID, chapters[2].ID, 2); !errors.Is(err, errGenerationPreviousDerivedNotReady) {
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
			engine := &generationTestEngine{
				run: func(_ context.Context, state *agents.GenerationState) (*agents.GenerationState, error) {
					state.Draft = validGeneratedContent()
					state.IsApproved = true
					return state, nil
				},
				publish: func(context.Context, *agents.GenerationState) error { return test.publishErr },
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
				retryEngine := &generationTestEngine{
					run: func(context.Context, *agents.GenerationState) (*agents.GenerationState, error) {
						t.Fatal("retry invoked chapter generation")
						return nil, nil
					},
					publish: func(_ context.Context, state *agents.GenerationState) error {
						if state.GenerationID != chapterRow.DerivedGenerationID || state.NovelID != fmt.Sprintf("%d", novelRow.ID) || state.ChapterID != fmt.Sprintf("%d", chapterRow.ID) || state.ChapterIndex != 1 || state.Draft != chapterRow.Content {
							t.Fatalf("retry state = %#v", state)
						}
						return nil
					},
				}
				retryServer := newServer(retryEngine, client)
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
				failedRetryServer := newServer(&generationTestEngine{publish: func(context.Context, *agents.GenerationState) error {
					return errors.New("still unavailable")
				}}, client)
				failedRetry := httptest.NewRecorder()
				failedRetryServer.router.ServeHTTP(
					failedRetry,
					httptest.NewRequest("POST", fmt.Sprintf("/api/v1/chapters/%d/derived/retry", chapterRow.ID), nil),
				)
				chapterRow, err = client.Chapter.Get(ctx, chapterRow.ID)
				if failedRetry.Code != 500 || err != nil || chapterRow.DerivedStatus != string(domain.DerivedStatusFailed) {
					t.Fatalf("failed retry = %d chapter=%#v error=%v", failedRetry.Code, chapterRow, err)
				}
			}
		})
	}
}
