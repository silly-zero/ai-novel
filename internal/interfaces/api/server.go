package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/ai-novel/studio/internal/application/workflows"
	"github.com/ai-novel/studio/internal/domain/agents"
	"github.com/ai-novel/studio/internal/domain/events"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

type Server struct {
	engine   *workflows.WorkflowEngine
	eventBus events.Bus
	router   *chi.Mux
}

func NewServer(engine *workflows.WorkflowEngine, eventBus events.Bus) *Server {
	s := &Server{
		engine:   engine,
		eventBus: eventBus,
		router:   chi.NewRouter(),
	}

	s.router.Use(middleware.Logger)
	s.router.Use(middleware.Recoverer)

	s.router.Get("/api/v1/novel/generate", s.HandleGenerateChapter)
	s.router.Get("/api/v1/novel/preview-context", s.HandlePreviewContext)

	return s
}

func (s *Server) Start(addr string) error {
	fmt.Printf("🚀 API Server started at %s\n", addr)
	return http.ListenAndServe(addr, s.router)
}

func (s *Server) HandleGenerateChapter(w http.ResponseWriter, r *http.Request) {
	// 1. 设置 SSE 响应头
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported!", http.StatusInternalServerError)
		return
	}

	novelID := r.URL.Query().Get("novel_id")
	outline := r.URL.Query().Get("outline")
	idea := r.URL.Query().Get("idea")
	editorNotes := r.URL.Query().Get("editor_notes")
	manualContext := r.URL.Query().Get("manual_context")
	chapterIndexStr := r.URL.Query().Get("chapter_index")
	chapterIndex := 1
	if chapterIndexStr != "" {
		fmt.Sscanf(chapterIndexStr, "%d", &chapterIndex)
	}

	if novelID == "" || (outline == "" && idea == "") {
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", "Missing novel_id and both outline/idea")
		flusher.Flush()
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// 2. 订阅 Token 生成事件
	tokenChan := make(chan string, 100)
	subID := s.eventBus.Subscribe("token.generated", func(ctx context.Context, event events.Event) error {
		e, ok := event.(events.TokenGeneratedEvent)
		if ok && e.NovelID == novelID {
			// 非阻塞发送，防止 EventBus 协程阻塞
			select {
			case tokenChan <- e.Token:
			default:
			}
		}
		return nil
	})
	// 确保在请求结束时取消订阅
	defer s.eventBus.Unsubscribe("token.generated", subID)

	// 3. 先推送 start，保证前端立即进入流式状态
	fmt.Fprintf(w, "event: start\ndata: %s\n\n", "Generation started")
	flusher.Flush()

	// 4. 预先生成场景卡与背景资料（只改不写），并推送元信息
	state := &agents.GenerationState{
		NovelID:       novelID,
		ChapterIndex:  chapterIndex,
		Outline:       outline,
		Idea:          idea,
		EditorNotes:   editorNotes,
		ManualContext: manualContext,
	}

	prepared, prepErr := s.engine.PrepareContext(ctx, state)
	if prepErr != nil {
		fmt.Fprintf(w, "event: error\ndata: %v\n\n", prepErr)
		flusher.Flush()
		return
	}

	meta := map[string]interface{}{
		"type":                 "context_meta",
		"novel_id":             prepared.NovelID,
		"chapter_index":        prepared.ChapterIndex,
		"editor_notes":         prepared.EditorNotes,
		"manual_context":       prepared.ManualContext,
		"full_outline_preview": truncate(prepared.FullOutline, 400),
		"outline_preview":      truncate(prepared.Outline, 300),
		"scene_card_preview":   truncate(prepared.SceneCard, 500),
		"context_preview":      truncate(prepared.Context, 800),
		"context_stats": map[string]int{
			"context_lines":    1 + strings.Count(prepared.Context, "\n"),
			"scene_card_lines": 1 + strings.Count(prepared.SceneCard, "\n"),
		},
	}
	metaBytes, _ := json.Marshal(meta)
	fmt.Fprintf(w, "event: context_meta\ndata: %s\n\n", string(metaBytes))
	flusher.Flush()

	// 5. 异步启动生成任务（writer/reviewer）
	errChan := make(chan error, 1)
	go func() {
		_, err := s.engine.RunChapterGeneration(ctx, prepared)
		if err != nil {
			errChan <- err
		}
		close(tokenChan)
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case err := <-errChan:
			fmt.Fprintf(w, "event: error\ndata: %v\n\n", err)
			flusher.Flush()
			return
		case token, ok := <-tokenChan:
			if !ok {
				fmt.Fprintf(w, "event: end\ndata: %s\n\n", "Generation finished")
				flusher.Flush()
				return
			}
			// 发送 SSE 格式数据
			data, _ := json.Marshal(map[string]string{"token": token})
			fmt.Fprintf(w, "data: %s\n\n", string(data))
			flusher.Flush()
		}
	}
}

func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || s == "" {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "..."
}

// HandlePreviewContext 仅生成“场景卡 + 背景资料 + 共创指令”的合成上下文，不进入写作
func (s *Server) HandlePreviewContext(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	novelID := r.URL.Query().Get("novel_id")
	outline := r.URL.Query().Get("outline")
	idea := r.URL.Query().Get("idea")
	editorNotes := r.URL.Query().Get("editor_notes")
	manualContext := r.URL.Query().Get("manual_context")
	chapterIndexStr := r.URL.Query().Get("chapter_index")
	chapterIndex := 1
	if chapterIndexStr != "" {
		fmt.Sscanf(chapterIndexStr, "%d", &chapterIndex)
	}

	if novelID == "" || (outline == "" && idea == "") {
		http.Error(w, "Missing novel_id and both outline/idea", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	state := &agents.GenerationState{
		NovelID:       novelID,
		ChapterIndex:  chapterIndex,
		Outline:       outline,
		Idea:          idea,
		EditorNotes:   editorNotes,
		ManualContext: manualContext,
	}

	res, err := s.engine.PrepareContext(ctx, state)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	payload := map[string]interface{}{
		"novel_id":       res.NovelID,
		"chapter_index":  res.ChapterIndex,
		"full_outline":   res.FullOutline,
		"outline":        res.Outline,
		"scene_card":     res.SceneCard,
		"context":        res.Context,
		"editor_notes":   res.EditorNotes,
		"manual_context": res.ManualContext,
	}
	enc := json.NewEncoder(w)
	_ = enc.Encode(payload)
}
