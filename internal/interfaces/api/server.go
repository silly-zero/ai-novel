package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

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

	// 3. 异步启动生成任务
	errChan := make(chan error, 1)
	go func() {
		state := &agents.GenerationState{
			NovelID: novelID,
			Outline: outline,
			Idea:    idea,
		}
		_, err := s.engine.RunChapterGeneration(ctx, state)
		if err != nil {
			errChan <- err
		}
		close(tokenChan)
	}()

	// 4. 将 Token 流式推向客户端
	fmt.Fprintf(w, "event: start\ndata: %s\n\n", "Generation started")
	flusher.Flush()

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
