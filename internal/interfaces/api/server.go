package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ai-novel/studio/ent"
	"github.com/ai-novel/studio/ent/chapter"
	"github.com/ai-novel/studio/ent/novel"
	"github.com/ai-novel/studio/internal/application/workflows"
	"github.com/ai-novel/studio/internal/domain/agents"
	"github.com/ai-novel/studio/internal/domain/events"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

type Server struct {
	engine   *workflows.WorkflowEngine
	eventBus events.Bus
	db       *ent.Client
	router   *chi.Mux
}

func NewServer(engine *workflows.WorkflowEngine, eventBus events.Bus, db *ent.Client) *Server {
	s := &Server{
		engine:   engine,
		eventBus: eventBus,
		db:       db,
		router:   chi.NewRouter(),
	}

	s.router.Use(middleware.Logger)
	s.router.Use(middleware.Recoverer)

	s.router.Get("/api/v1/novels", s.HandleListNovels)
	s.router.Post("/api/v1/novels", s.HandleCreateNovel)
	s.router.Options("/api/v1/novels", s.HandleOptions)
	s.router.Get("/api/v1/novels/{id}", s.HandleGetNovel)
	s.router.Put("/api/v1/novels/{id}", s.HandleUpdateNovel)
	s.router.Options("/api/v1/novels/{id}", s.HandleOptions)
	s.router.Get("/api/v1/novels/{id}/chapters", s.HandleListChapters)
	s.router.Post("/api/v1/novels/{id}/chapters", s.HandleCreateChapter)
	s.router.Options("/api/v1/novels/{id}/chapters", s.HandleOptions)
	s.router.Get("/api/v1/chapters/{id}", s.HandleGetChapter)
	s.router.Put("/api/v1/chapters/{id}", s.HandleUpdateChapter)
	s.router.Delete("/api/v1/chapters/{id}", s.HandleDeleteChapter)
	s.router.Options("/api/v1/chapters/{id}", s.HandleOptions)
	s.router.Get("/api/v1/novel/generate", s.HandleGenerateChapter)
	s.router.Get("/api/v1/novel/preview-context", s.HandlePreviewContext)

	return s
}

type NovelSummary struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	Status      string    `json:"status"`
	Tags        []string  `json:"tags,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type NovelDetail struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	Idea        string    `json:"idea,omitempty"`
	Outline     string    `json:"outline,omitempty"`
	Status      string    `json:"status"`
	Tags        []string  `json:"tags,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type ChapterItem struct {
	ID        string    `json:"id"`
	NovelID   string    `json:"novel_id"`
	Title     string    `json:"title"`
	Content   string    `json:"content"`
	WordCount int       `json:"word_count"`
	Order     int       `json:"order"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type CreateNovelRequest struct {
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Type        string   `json:"type,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

type UpdateNovelRequest struct {
	Idea    *string `json:"idea,omitempty"`
	Outline *string `json:"outline,omitempty"`
}

type CreateChapterRequest struct {
	Title   string `json:"title,omitempty"`
	Content string `json:"content,omitempty"`
	Order   int    `json:"order,omitempty"`
	Status  string `json:"status,omitempty"`
}

type UpdateChapterRequest struct {
	Title   *string `json:"title,omitempty"`
	Content *string `json:"content,omitempty"`
	Order   *int    `json:"order,omitempty"`
	Status  *string `json:"status,omitempty"`
}

func (s *Server) HandleListNovels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if s.db == nil {
		http.Error(w, "database not configured", http.StatusInternalServerError)
		return
	}

	rows, err := s.db.Novel.
		Query().
		Order(ent.Desc(novel.FieldUpdatedAt), ent.Desc(novel.FieldCreatedAt)).
		All(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	items := make([]NovelSummary, 0, len(rows))
	for _, n := range rows {
		items = append(items, NovelSummary{
			ID:          fmt.Sprintf("%d", n.ID),
			Title:       n.Title,
			Description: n.Description,
			Status:      n.Status,
			Tags:        n.Tags,
			CreatedAt:   n.CreatedAt,
			UpdatedAt:   n.UpdatedAt,
		})
	}

	_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
}

func (s *Server) HandleOptions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) HandleCreateNovel(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if s.db == nil {
		http.Error(w, "database not configured", http.StatusInternalServerError)
		return
	}

	var req CreateNovelRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid json: %v", err), http.StatusBadRequest)
		return
	}

	title := strings.TrimSpace(req.Title)
	if title == "" {
		http.Error(w, "title is required", http.StatusBadRequest)
		return
	}

	description := strings.TrimSpace(req.Description)
	novelType := strings.TrimSpace(req.Type)
	tags := make([]string, 0, len(req.Tags)+1)
	if novelType != "" {
		tags = append(tags, novelType)
	}
	for _, t := range req.Tags {
		tt := strings.TrimSpace(t)
		if tt == "" {
			continue
		}
		if novelType != "" && tt == novelType {
			continue
		}
		tags = append(tags, tt)
	}

	row, err := s.db.Novel.
		Create().
		SetTitle(title).
		SetDescription(description).
		SetIdea("").
		SetOutline("").
		SetStatus("Draft").
		SetTags(tags).
		Save(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	item := NovelSummary{
		ID:          fmt.Sprintf("%d", row.ID),
		Title:       row.Title,
		Description: row.Description,
		Status:      row.Status,
		Tags:        row.Tags,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}

	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"item": item})
}

func (s *Server) HandleGetNovel(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if s.db == nil {
		http.Error(w, "database not configured", http.StatusInternalServerError)
		return
	}

	id, parseErr := parseIntParam(chi.URLParam(r, "id"))
	if parseErr != nil {
		http.Error(w, parseErr.Error(), http.StatusBadRequest)
		return
	}

	row, err := s.db.Novel.
		Query().
		Where(novel.ID(id)).
		WithChapters(func(q *ent.ChapterQuery) {
			q.Order(ent.Asc(chapter.FieldOrder), ent.Asc(chapter.FieldCreatedAt))
		}).
		Only(r.Context())
	if err != nil {
		if ent.IsNotFound(err) {
			http.Error(w, "novel not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	item := NovelDetail{
		ID:          fmt.Sprintf("%d", row.ID),
		Title:       row.Title,
		Description: row.Description,
		Idea:        row.Idea,
		Outline:     row.Outline,
		Status:      row.Status,
		Tags:        row.Tags,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}

	chapters := make([]ChapterItem, 0, len(row.Edges.Chapters))
	for _, c := range row.Edges.Chapters {
		chapters = append(chapters, ChapterItem{
			ID:        fmt.Sprintf("%d", c.ID),
			NovelID:   item.ID,
			Title:     c.Title,
			Content:   c.Content,
			WordCount: c.WordCount,
			Order:     c.Order,
			Status:    c.Status,
			CreatedAt: c.CreatedAt,
			UpdatedAt: c.UpdatedAt,
		})
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"item":     item,
		"chapters": chapters,
	})
}

func (s *Server) HandleUpdateNovel(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if s.db == nil {
		http.Error(w, "database not configured", http.StatusInternalServerError)
		return
	}

	id, parseErr := parseIntParam(chi.URLParam(r, "id"))
	if parseErr != nil {
		http.Error(w, parseErr.Error(), http.StatusBadRequest)
		return
	}

	var req UpdateNovelRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 5<<20))
	dec.DisallowUnknownFields()
	if decodeErr := dec.Decode(&req); decodeErr != nil {
		http.Error(w, fmt.Sprintf("invalid json: %v", decodeErr), http.StatusBadRequest)
		return
	}

	upd := s.db.Novel.UpdateOneID(id)
	if req.Idea != nil {
		upd.SetIdea(strings.TrimSpace(*req.Idea))
	}
	if req.Outline != nil {
		upd.SetOutline(strings.TrimSpace(*req.Outline))
	}

	row, saveErr := upd.Save(r.Context())
	if saveErr != nil {
		if ent.IsNotFound(saveErr) {
			http.Error(w, "novel not found", http.StatusNotFound)
			return
		}
		http.Error(w, saveErr.Error(), http.StatusInternalServerError)
		return
	}

	item := NovelDetail{
		ID:          fmt.Sprintf("%d", row.ID),
		Title:       row.Title,
		Description: row.Description,
		Idea:        row.Idea,
		Outline:     row.Outline,
		Status:      row.Status,
		Tags:        row.Tags,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}

	_ = json.NewEncoder(w).Encode(map[string]any{"item": item})
}

func (s *Server) HandleListChapters(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if s.db == nil {
		http.Error(w, "database not configured", http.StatusInternalServerError)
		return
	}

	novelID, err := parseIntParam(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	limit := 50
	offset := 0
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		if n, convErr := strconv.Atoi(v); convErr == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	if v := strings.TrimSpace(r.URL.Query().Get("offset")); v != "" {
		if n, convErr := strconv.Atoi(v); convErr == nil && n >= 0 {
			offset = n
		}
	}

	rows, err := s.db.Chapter.
		Query().
		Where(chapter.HasNovelWith(novel.ID(novelID))).
		Order(ent.Asc(chapter.FieldOrder), ent.Asc(chapter.FieldCreatedAt)).
		Limit(limit).
		Offset(offset).
		All(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	items := make([]ChapterItem, 0, len(rows))
	for _, c := range rows {
		items = append(items, ChapterItem{
			ID:        fmt.Sprintf("%d", c.ID),
			NovelID:   fmt.Sprintf("%d", novelID),
			Title:     c.Title,
			Content:   c.Content,
			WordCount: c.WordCount,
			Order:     c.Order,
			Status:    c.Status,
			CreatedAt: c.CreatedAt,
			UpdatedAt: c.UpdatedAt,
		})
	}

	_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
}

func (s *Server) HandleGetChapter(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if s.db == nil {
		http.Error(w, "database not configured", http.StatusInternalServerError)
		return
	}

	id, err := parseIntParam(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	row, err := s.db.Chapter.
		Query().
		Where(chapter.ID(id)).
		WithNovel().
		Only(r.Context())
	if err != nil {
		if ent.IsNotFound(err) {
			http.Error(w, "chapter not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	novelID := ""
	if row.Edges.Novel != nil {
		novelID = fmt.Sprintf("%d", row.Edges.Novel.ID)
	}

	item := ChapterItem{
		ID:        fmt.Sprintf("%d", row.ID),
		NovelID:   novelID,
		Title:     row.Title,
		Content:   row.Content,
		WordCount: row.WordCount,
		Order:     row.Order,
		Status:    row.Status,
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}

	_ = json.NewEncoder(w).Encode(map[string]any{"item": item})
}

func (s *Server) HandleCreateChapter(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if s.db == nil {
		http.Error(w, "database not configured", http.StatusInternalServerError)
		return
	}

	novelID, err := parseIntParam(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var req CreateChapterRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 5<<20))
	dec.DisallowUnknownFields()
	if decodeErr := dec.Decode(&req); decodeErr != nil {
		http.Error(w, fmt.Sprintf("invalid json: %v", decodeErr), http.StatusBadRequest)
		return
	}

	order := req.Order
	if order <= 0 {
		last, queryErr := s.db.Chapter.
			Query().
			Where(chapter.HasNovelWith(novel.ID(novelID))).
			Order(ent.Desc(chapter.FieldOrder)).
			First(r.Context())
		if queryErr == nil && last != nil {
			order = last.Order + 1
		} else {
			order = 1
		}
	}

	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = chapterTitle(order)
	}
	content := req.Content
	status := strings.TrimSpace(req.Status)
	if status == "" {
		status = "Draft"
	}

	row, err := s.db.Chapter.
		Create().
		SetNovelID(novelID).
		SetTitle(title).
		SetContent(content).
		SetWordCount(wordCountOf(content)).
		SetOrder(order).
		SetStatus(status).
		Save(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	item := ChapterItem{
		ID:        fmt.Sprintf("%d", row.ID),
		NovelID:   fmt.Sprintf("%d", novelID),
		Title:     row.Title,
		Content:   row.Content,
		WordCount: row.WordCount,
		Order:     row.Order,
		Status:    row.Status,
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}

	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"item": item})
}

func (s *Server) HandleUpdateChapter(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if s.db == nil {
		http.Error(w, "database not configured", http.StatusInternalServerError)
		return
	}

	id, err := parseIntParam(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var req UpdateChapterRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 10<<20))
	dec.DisallowUnknownFields()
	if decodeErr := dec.Decode(&req); decodeErr != nil {
		http.Error(w, fmt.Sprintf("invalid json: %v", decodeErr), http.StatusBadRequest)
		return
	}

	upd := s.db.Chapter.UpdateOneID(id)
	if req.Title != nil {
		upd.SetTitle(strings.TrimSpace(*req.Title))
	}
	if req.Order != nil {
		if *req.Order <= 0 {
			http.Error(w, "order must be > 0", http.StatusBadRequest)
			return
		}
		upd.SetOrder(*req.Order)
	}
	if req.Status != nil {
		upd.SetStatus(strings.TrimSpace(*req.Status))
	}
	if req.Content != nil {
		upd.SetContent(*req.Content)
		upd.SetWordCount(wordCountOf(*req.Content))
	}

	row, saveErr := upd.Save(r.Context())
	if saveErr != nil {
		if ent.IsNotFound(saveErr) {
			http.Error(w, "chapter not found", http.StatusNotFound)
			return
		}
		http.Error(w, saveErr.Error(), http.StatusInternalServerError)
		return
	}

	novelID := ""
	n, queryErr := row.QueryNovel().Only(r.Context())
	if queryErr == nil && n != nil {
		novelID = fmt.Sprintf("%d", n.ID)
	}

	item := ChapterItem{
		ID:        fmt.Sprintf("%d", row.ID),
		NovelID:   novelID,
		Title:     row.Title,
		Content:   row.Content,
		WordCount: row.WordCount,
		Order:     row.Order,
		Status:    row.Status,
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}

	_ = json.NewEncoder(w).Encode(map[string]any{"item": item})
}

func (s *Server) HandleDeleteChapter(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if s.db == nil {
		http.Error(w, "database not configured", http.StatusInternalServerError)
		return
	}

	id, err := parseIntParam(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := s.db.Chapter.DeleteOneID(id).Exec(r.Context()); err != nil {
		if ent.IsNotFound(err) {
			http.Error(w, "chapter not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) Start(addr string) error {
	fmt.Printf("🚀 API Server started at %s\n", addr)
	return http.ListenAndServe(addr, s.router)
}

func parseIntParam(v string) (int, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, fmt.Errorf("empty id")
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid id: %q", v)
	}
	return n, nil
}

func wordCountOf(s string) int {
	return len([]rune(strings.TrimSpace(s)))
}

func chapterTitle(index int) string {
	if index <= 0 {
		return "未命名章节"
	}
	return fmt.Sprintf("第%d章", index)
}

func (s *Server) ensureChapterRecord(ctx context.Context, novelID int, chapterIndex int) (int, error) {
	if s.db == nil {
		return 0, fmt.Errorf("database not configured")
	}
	if novelID <= 0 {
		return 0, fmt.Errorf("invalid novel id")
	}
	if chapterIndex <= 0 {
		return 0, fmt.Errorf("invalid chapter index")
	}

	row, queryErr := s.db.Chapter.
		Query().
		Where(
			chapter.OrderEQ(chapterIndex),
			chapter.HasNovelWith(novel.ID(novelID)),
		).
		Only(ctx)
	if queryErr == nil && row != nil {
		if execErr := s.db.Chapter.
			UpdateOneID(row.ID).
			SetTitle(chapterTitle(chapterIndex)).
			SetContent("").
			SetWordCount(0).
			SetStatus("Generating").
			Exec(ctx); execErr != nil {
			return 0, execErr
		}
		return row.ID, nil
	}
	if queryErr != nil && !ent.IsNotFound(queryErr) {
		return 0, queryErr
	}

	created, err := s.db.Chapter.
		Create().
		SetNovelID(novelID).
		SetTitle(chapterTitle(chapterIndex)).
		SetContent("").
		SetWordCount(0).
		SetOrder(chapterIndex).
		SetStatus("Generating").
		Save(ctx)
	if err != nil {
		return 0, err
	}
	return created.ID, nil
}

func (s *Server) HandleGenerateChapter(w http.ResponseWriter, r *http.Request) {
	if s.engine == nil {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		http.Error(w, "engine not configured", http.StatusInternalServerError)
		return
	}

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
	existingOutline := strings.TrimSpace(r.URL.Query().Get("existing_outline"))
	outlineStart, _ := strconv.Atoi(r.URL.Query().Get("outline_start"))
	outlineEnd, _ := strconv.Atoi(r.URL.Query().Get("outline_end"))
	chapterIDStr := r.URL.Query().Get("chapter_id")
	persistStr := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("persist")))
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

	novelIDInt, err := parseIntParam(novelID)
	if err != nil {
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", err.Error())
		flusher.Flush()
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	persist := true
	if persistStr == "0" || persistStr == "false" || persistStr == "no" {
		persist = false
	}

	chapterIDInt := 0
	if s.db != nil && persist {
		saveCtx, saveCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		if strings.TrimSpace(chapterIDStr) != "" {
			chapterIDInt, err = parseIntParam(chapterIDStr)
			if err != nil {
				saveCancel()
				fmt.Fprintf(w, "event: error\ndata: %s\n\n", err.Error())
				flusher.Flush()
				return
			}
			_, queryErr := s.db.Chapter.
				Query().
				Where(
					chapter.ID(chapterIDInt),
					chapter.HasNovelWith(novel.ID(novelIDInt)),
				).
				Only(saveCtx)
			if queryErr != nil {
				saveCancel()
				if ent.IsNotFound(queryErr) {
					fmt.Fprintf(w, "event: error\ndata: %s\n\n", "chapter not found")
					flusher.Flush()
					return
				}
				fmt.Fprintf(w, "event: error\ndata: %v\n\n", queryErr)
				flusher.Flush()
				return
			}
			if execErr := s.db.Chapter.
				UpdateOneID(chapterIDInt).
				SetContent("").
				SetWordCount(0).
				SetStatus("Generating").
				Exec(saveCtx); execErr != nil {
				saveCancel()
				fmt.Fprintf(w, "event: error\ndata: %v\n\n", execErr)
				flusher.Flush()
				return
			}
		} else {
			chapterIDInt, err = s.ensureChapterRecord(saveCtx, novelIDInt, chapterIndex)
		}
		saveCancel()
		if err != nil {
			fmt.Fprintf(w, "event: error\ndata: %v\n\n", err)
			flusher.Flush()
			return
		}
	}

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
	chapterID := ""
	if chapterIDInt > 0 {
		chapterID = fmt.Sprintf("%d", chapterIDInt)
	}
	state := &agents.GenerationState{
		NovelID:         novelID,
		ChapterID:       chapterID,
		ChapterIndex:    chapterIndex,
		Idea:            idea,
		FullOutline:     outline,
		EditorNotes:     editorNotes,
		ManualContext:   manualContext,
		ExistingOutline: existingOutline,
		OutlineStart:    outlineStart,
		OutlineEnd:      outlineEnd,
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
		"chapter_id":           chapterID,
		"persist":              persist,
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
		finalState, err := s.engine.RunChapterGeneration(ctx, prepared)
		if err != nil {
			errChan <- err
			close(tokenChan)
			return
		}

		if s.db != nil && persist && finalState != nil && chapterIDInt > 0 {
			saveCtx, saveCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			_, _ = s.db.Chapter.
				UpdateOneID(chapterIDInt).
				SetTitle(chapterTitle(finalState.ChapterIndex)).
				SetContent(finalState.Draft).
				SetWordCount(wordCountOf(finalState.Draft)).
				SetStatus("Draft").
				Save(saveCtx)
			saveCancel()
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
	if s.engine == nil {
		http.Error(w, "engine not configured", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	novelID := r.URL.Query().Get("novel_id")
	outline := strings.TrimSpace(r.URL.Query().Get("outline"))
	idea := strings.TrimSpace(r.URL.Query().Get("idea"))
	editorNotes := strings.TrimSpace(r.URL.Query().Get("editor_notes"))
	manualContext := strings.TrimSpace(r.URL.Query().Get("manual_context"))
	existingOutline := strings.TrimSpace(r.URL.Query().Get("existing_outline"))
	outlineStart, _ := strconv.Atoi(r.URL.Query().Get("outline_start"))
	outlineEnd, _ := strconv.Atoi(r.URL.Query().Get("outline_end"))

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
		NovelID:         novelID,
		ChapterIndex:    chapterIndex,
		FullOutline:     outline,
		Idea:            idea,
		EditorNotes:     editorNotes,
		ManualContext:   manualContext,
		ExistingOutline: existingOutline,
		OutlineStart:    outlineStart,
		OutlineEnd:      outlineEnd,
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
