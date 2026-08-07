package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"entgo.io/ent/dialect/sql"
	"entgo.io/ent/dialect/sql/sqljson"
	"github.com/ai-novel/studio/ent"
	"github.com/ai-novel/studio/ent/chapter"
	"github.com/ai-novel/studio/ent/novel"
	"github.com/ai-novel/studio/ent/predicate"
	"github.com/ai-novel/studio/internal/application/workflows"
	"github.com/ai-novel/studio/internal/domain/agents"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

type generationEngine interface {
	PrepareContext(context.Context, *agents.GenerationState) (*agents.GenerationState, error)
	RunChapterGeneration(context.Context, *agents.GenerationState) (*agents.GenerationState, error)
	PublishChapterGenerated(context.Context, *agents.GenerationState) error
	ExtractContinuity(context.Context, *agents.GenerationState) (*agents.GenerationState, error)
}

type generationChapterTarget struct {
	ID                 int
	Title              string
	Content            string
	WordCount          int
	Order              int
	Status             string
	LastBeat           string
	OpenLoops          []string
	NextAction         string
	PreviousContinuity agents.ContinuityPacket
	UpdatedAt          time.Time
}

type generationChapterStore interface {
	Prepare(context.Context, int, int, int) (*generationChapterTarget, error)
	Save(context.Context, *generationChapterTarget, *agents.GenerationState) error
}

type entGenerationChapterStore struct {
	client *ent.Client
}

var errGenerationChapterChanged = errors.New("chapter changed during generation")

func (s *entGenerationChapterStore) Prepare(
	ctx context.Context,
	novelID int,
	chapterID int,
	chapterIndex int,
) (*generationChapterTarget, error) {
	if novelID <= 0 {
		return nil, errors.New("invalid novel id")
	}
	if chapterID <= 0 && chapterIndex <= 0 {
		return nil, errors.New("invalid chapter index")
	}
	query := s.client.Chapter.Query()
	if chapterID > 0 {
		query = query.Where(
			chapter.ID(chapterID),
			chapter.HasNovelWith(novel.ID(novelID)),
		)
	} else {
		query = query.Where(
			chapter.OrderEQ(chapterIndex),
			chapter.HasNovelWith(novel.ID(novelID)),
		)
	}

	row, err := query.Only(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, err
	}
	if err == nil {
		target := generationChapterTargetFromRow(row)
		if err := s.loadPreviousContinuity(ctx, novelID, target); err != nil {
			return nil, err
		}
		return target, nil
	}
	if chapterID > 0 {
		return nil, errors.New("chapter not found")
	}

	row, err = s.client.Chapter.
		Create().
		SetNovelID(novelID).
		SetTitle(chapterTitle(chapterIndex)).
		SetContent("").
		SetWordCount(0).
		SetOrder(chapterIndex).
		SetStatus("Draft").
		Save(ctx)
	if err != nil {
		return nil, err
	}
	target := generationChapterTargetFromRow(row)
	if err := s.loadPreviousContinuity(ctx, novelID, target); err != nil {
		return nil, err
	}
	return target, nil
}

func (s *entGenerationChapterStore) loadPreviousContinuity(
	ctx context.Context,
	novelID int,
	target *generationChapterTarget,
) error {
	if target == nil || target.Order <= 1 {
		return nil
	}
	previous, err := s.client.Chapter.Query().Where(
		chapter.OrderEQ(target.Order-1),
		chapter.HasNovelWith(novel.ID(novelID)),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	target.PreviousContinuity = agents.ContinuityPacket{
		LastBeat:   strings.TrimSpace(previous.LastBeat),
		OpenLoops:  append([]string(nil), previous.OpenLoops...),
		NextAction: strings.TrimSpace(previous.NextAction),
	}
	return nil
}

func (s *entGenerationChapterStore) Save(
	ctx context.Context,
	target *generationChapterTarget,
	state *agents.GenerationState,
) error {
	_, err := s.client.Chapter.
		UpdateOneID(target.ID).
		Where(
			chapter.TitleEQ(target.Title),
			chapter.ContentEQ(target.Content),
			chapter.WordCountEQ(target.WordCount),
			chapter.OrderEQ(target.Order),
			chapter.StatusEQ(target.Status),
			chapter.LastBeatEQ(target.LastBeat),
			predicate.Chapter(func(selector *sql.Selector) {
				openLoopsPredicate := sqljson.ValueEQ(chapter.FieldOpenLoops, target.OpenLoops)
				if len(target.OpenLoops) == 0 {
					openLoopsPredicate = sql.Or(
						sql.IsNull(chapter.FieldOpenLoops),
						openLoopsPredicate,
					)
				}
				selector.Where(openLoopsPredicate)
			}),
			chapter.NextActionEQ(target.NextAction),
			chapter.UpdatedAtEQ(target.UpdatedAt),
		).
		SetContent(state.Draft).
		SetWordCount(wordCountOf(state.Draft)).
		SetStatus("Draft").
		SetLastBeat(state.Continuity.LastBeat).
		SetOpenLoops(state.Continuity.OpenLoops).
		SetNextAction(state.Continuity.NextAction).
		Save(ctx)
	if ent.IsNotFound(err) {
		return errGenerationChapterChanged
	}
	return err
}

func generationChapterTargetFromRow(row *ent.Chapter) *generationChapterTarget {
	return &generationChapterTarget{
		ID:         row.ID,
		Title:      row.Title,
		Content:    row.Content,
		WordCount:  row.WordCount,
		Order:      row.Order,
		Status:     row.Status,
		LastBeat:   row.LastBeat,
		OpenLoops:  append([]string(nil), row.OpenLoops...),
		NextAction: row.NextAction,
		UpdatedAt:  row.UpdatedAt,
	}
}

type activeGeneration struct {
	generationID string
	ctx          context.Context
	cancel       context.CancelCauseFunc
	finished     bool
}

type generationGuard struct {
	mu     sync.Mutex
	active map[int]activeGeneration
}

func newGenerationGuard() *generationGuard {
	return &generationGuard{active: make(map[int]activeGeneration)}
}

func (g *generationGuard) acquire(
	novelID int,
	generationID string,
	ctx context.Context,
	cancel context.CancelCauseFunc,
) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, exists := g.active[novelID]; exists {
		return false
	}
	g.active[novelID] = activeGeneration{
		generationID: generationID,
		ctx:          ctx,
		cancel:       cancel,
	}
	return true
}

func (g *generationGuard) cancel(
	novelID int,
	generationID string,
	cause error,
) generationCancelResult {
	g.mu.Lock()
	defer g.mu.Unlock()

	active, exists := g.active[novelID]
	if !exists {
		return generationCancelNotFound
	}
	if active.generationID != generationID {
		return generationCancelConflict
	}
	if active.finished {
		return generationCancelConflict
	}
	active.cancel(cause)
	return generationCancelAccepted
}

func (g *generationGuard) finish(
	novelID int,
	generationID string,
) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	active, exists := g.active[novelID]
	if !exists || active.generationID != generationID {
		return nil
	}
	active.finished = true
	g.active[novelID] = active
	return context.Cause(active.ctx)
}

func (g *generationGuard) release(novelID int, generationID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	active, exists := g.active[novelID]
	if !exists || active.generationID != generationID {
		return
	}
	delete(g.active, novelID)
}

type generationCancelResult int

const (
	generationCancelAccepted generationCancelResult = iota
	generationCancelNotFound
	generationCancelConflict
)

var (
	errGenerationCancelled = errors.New("generation cancelled by user")
	errGenerationProtocol  = errors.New("invalid generation stream event")
)

type ServerConfig struct {
	ListenAddr               string
	CorsOrigins              []string
	MaxConcurrentGenerations int
	ReadHeaderTimeout        time.Duration
	ReadTimeout              time.Duration
	WriteTimeout             time.Duration
	IdleTimeout              time.Duration
	GenerationTimeout        time.Duration
}

func defaultServerConfig() ServerConfig {
	return ServerConfig{
		ListenAddr:               "127.0.0.1:8081",
		CorsOrigins:              []string{"http://localhost:5173", "http://127.0.0.1:5173"},
		MaxConcurrentGenerations: 2,
		ReadHeaderTimeout:        5 * time.Second,
		ReadTimeout:              15 * time.Second,
		WriteTimeout:             30 * time.Second,
		IdleTimeout:              60 * time.Second,
		GenerationTimeout:        30 * time.Minute,
	}
}

type modelCapacity struct {
	mu      sync.Mutex
	closing bool
	slots   chan struct{}
	active  sync.WaitGroup
}

func newModelCapacity(limit int) *modelCapacity {
	return &modelCapacity{slots: make(chan struct{}, limit)}
}

func (c *modelCapacity) tryAcquire() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return false
	}
	select {
	case c.slots <- struct{}{}:
		c.active.Add(1)
		return true
	default:
		return false
	}
}

func (c *modelCapacity) release() {
	<-c.slots
	c.active.Done()
}

func (c *modelCapacity) closeAndWait(ctx context.Context) error {
	c.mu.Lock()
	c.closing = true
	c.mu.Unlock()
	done := make(chan struct{})
	go func() {
		c.active.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type Server struct {
	engine          generationEngine
	db              *ent.Client
	chapterStore    generationChapterStore
	router          *chi.Mux
	generationGuard *generationGuard
	config          ServerConfig
	corsOrigins     map[string]struct{}
	modelCapacity   *modelCapacity
	lifecycleCtx    context.Context
	cancelLifecycle context.CancelFunc
}

func NewServer(engine *workflows.WorkflowEngine, db *ent.Client, configs ...ServerConfig) *Server {
	var engineAdapter generationEngine
	if engine != nil {
		engineAdapter = engine
	}
	return newServerWithConfig(engineAdapter, db, firstServerConfig(configs))
}

func firstServerConfig(configs []ServerConfig) ServerConfig {
	if len(configs) == 0 {
		return defaultServerConfig()
	}
	return configs[0]
}

func newServer(engine generationEngine, db *ent.Client) *Server {
	return newServerWithConfig(engine, db, defaultServerConfig())
}

func newServerWithConfig(engine generationEngine, db *ent.Client, cfg ServerConfig) *Server {
	lifecycleCtx, cancelLifecycle := context.WithCancel(context.Background())
	origins := make(map[string]struct{}, len(cfg.CorsOrigins))
	for _, origin := range cfg.CorsOrigins {
		origins[origin] = struct{}{}
	}
	s := &Server{
		engine:          engine,
		db:              db,
		router:          chi.NewRouter(),
		generationGuard: newGenerationGuard(),
		config:          cfg,
		corsOrigins:     origins,
		modelCapacity:   newModelCapacity(cfg.MaxConcurrentGenerations),
		lifecycleCtx:    lifecycleCtx,
		cancelLifecycle: cancelLifecycle,
	}
	if db != nil {
		s.chapterStore = &entGenerationChapterStore{client: db}
	}

	s.router.Use(middleware.Logger)
	s.router.Use(middleware.Recoverer)
	s.router.Use(s.corsMiddleware)
	s.router.Use(s.lifecycleMiddleware)
	s.router.Options("/*", s.HandleOptions)

	s.router.Get("/api/v1/novels", s.HandleListNovels)
	s.router.Post("/api/v1/novels", s.HandleCreateNovel)
	s.router.Get("/api/v1/novels/{id}", s.HandleGetNovel)
	s.router.Put("/api/v1/novels/{id}", s.HandleUpdateNovel)
	s.router.Get("/api/v1/novels/{id}/chapters", s.HandleListChapters)
	s.router.Post("/api/v1/novels/{id}/chapters", s.HandleCreateChapter)
	s.router.Get("/api/v1/chapters/{id}", s.HandleGetChapter)
	s.router.Put("/api/v1/chapters/{id}", s.HandleUpdateChapter)
	s.router.Delete("/api/v1/chapters/{id}", s.HandleDeleteChapter)
	s.router.Get("/api/v1/novel/generate", s.HandleGenerateChapter)
	s.router.Post("/api/v1/novels/{id}/generate/cancel", s.HandleCancelGeneration)
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

func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if origin != "" {
			w.Header().Add("Vary", "Origin")
			if _, allowed := s.corsOrigins[origin]; allowed {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept")
			} else {
				http.Error(w, "origin not allowed", http.StatusForbidden)
				return
			}
		} else if strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "cross-site") {
			http.Error(w, "cross-site request not allowed", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) lifecycleMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithCancelCause(r.Context())
		stop := context.AfterFunc(s.lifecycleCtx, func() {
			cancel(context.Cause(s.lifecycleCtx))
		})
		defer func() {
			stop()
			cancel(context.Canceled)
		}()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) HandleListNovels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

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

func (s *Server) HandleOptions(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) HandleCreateNovel(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

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

func (s *Server) HTTPServer() *http.Server {
	return &http.Server{
		Addr:              s.config.ListenAddr,
		Handler:           s.router,
		ReadHeaderTimeout: s.config.ReadHeaderTimeout,
		ReadTimeout:       s.config.ReadTimeout,
		WriteTimeout:      s.config.WriteTimeout,
		IdleTimeout:       s.config.IdleTimeout,
		MaxHeaderBytes:    1 << 20,
	}
}

func (s *Server) Shutdown(ctx context.Context, httpServer *http.Server) error {
	s.cancelLifecycle()
	shutdownErr := httpServer.Shutdown(ctx)
	capacityErr := s.modelCapacity.closeAndWait(ctx)
	if shutdownErr != nil {
		return shutdownErr
	}
	return capacityErr
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

type CancelGenerationRequest struct {
	GenerationID string `json:"generation_id"`
}

type generationStatus string

const (
	generationStatusSuccess   generationStatus = "success"
	generationStatusError     generationStatus = "error"
	generationStatusCancelled generationStatus = "cancelled"
)

type generationResult struct {
	GenerationID string           `json:"generation_id"`
	Status       generationStatus `json:"status"`
	Message      string           `json:"message,omitempty"`
}

const generationSSEWriteTimeout = 15 * time.Second

type generationSSEWriter struct {
	writer       http.ResponseWriter
	controller   *http.ResponseController
	terminalSent bool
}

func newGenerationSSEWriter(
	writer http.ResponseWriter,
	controller *http.ResponseController,
) *generationSSEWriter {
	return &generationSSEWriter{writer: writer, controller: controller}
}

func (w *generationSSEWriter) send(event string, payload any) error {
	if err := w.controller.SetWriteDeadline(time.Now().Add(generationSSEWriteTimeout)); err != nil &&
		!errors.Is(err, http.ErrNotSupported) {
		return err
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err = fmt.Fprintf(
		w.writer,
		"event: %s\ndata: %s\n\n",
		event,
		data,
	); err != nil {
		return err
	}
	if err := w.controller.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	return nil
}

func (w *generationSSEWriter) terminal(result generationResult) error {
	if w.terminalSent {
		return errors.New("generation terminal already sent")
	}
	w.terminalSent = true
	return w.send("terminal", result)
}

func classifyGenerationResult(
	generationID string,
	cause error,
	runErr error,
	finalState *agents.GenerationState,
) generationResult {
	if runErr == nil && finalState != nil &&
		!errors.Is(cause, errGenerationCancelled) &&
		!errors.Is(cause, errGenerationProtocol) &&
		!errors.Is(cause, context.DeadlineExceeded) {
		cause = nil
	}
	switch {
	case errors.Is(cause, errGenerationCancelled):
		return generationResult{
			GenerationID: generationID,
			Status:       generationStatusCancelled,
			Message:      "生成已取消",
		}
	case errors.Is(cause, errGenerationProtocol):
		return generationResult{
			GenerationID: generationID,
			Status:       generationStatusError,
			Message:      cause.Error(),
		}
	case cause != nil:
		return generationResult{
			GenerationID: generationID,
			Status:       generationStatusError,
			Message:      cause.Error(),
		}
	case runErr != nil:
		return generationResult{
			GenerationID: generationID,
			Status:       generationStatusError,
			Message:      runErr.Error(),
		}
	case finalState == nil:
		return generationResult{
			GenerationID: generationID,
			Status:       generationStatusError,
			Message:      "generation returned no final state",
		}
	default:
		return generationResult{
			GenerationID: generationID,
			Status:       generationStatusSuccess,
			Message:      "生成完成",
		}
	}
}

func (s *Server) HandleCancelGeneration(
	w http.ResponseWriter,
	r *http.Request,
) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	novelID, err := parseIntParam(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var req CancelGenerationRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid json: %v", err), http.StatusBadRequest)
		return
	}
	req.GenerationID = strings.TrimSpace(req.GenerationID)
	if req.GenerationID == "" {
		http.Error(w, "generation_id is required", http.StatusBadRequest)
		return
	}

	switch s.generationGuard.cancel(
		novelID,
		req.GenerationID,
		errGenerationCancelled,
	) {
	case generationCancelAccepted:
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"generation_id": req.GenerationID,
			"status":        "cancelling",
		})
	case generationCancelNotFound:
		http.Error(w, "no active generation for novel", http.StatusNotFound)
	case generationCancelConflict:
		http.Error(w, "generation_id does not match active generation", http.StatusConflict)
	}
}

func (s *Server) HandleGenerateChapter(w http.ResponseWriter, r *http.Request) {
	if s.engine == nil {
		http.Error(w, "engine not configured", http.StatusInternalServerError)
		return
	}

	novelIDRaw := strings.TrimSpace(r.URL.Query().Get("novel_id"))
	if novelIDRaw == "" {
		http.Error(w, "Missing novel_id", http.StatusBadRequest)
		return
	}
	novelIDInt, err := parseIntParam(novelIDRaw)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	novelID := strconv.Itoa(novelIDInt)
	generationID, err := agents.NewGenerationID()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	deadlineCtx, deadlineCancel := context.WithTimeout(r.Context(), s.config.GenerationTimeout)
	defer deadlineCancel()
	generationDeadline, _ := deadlineCtx.Deadline()
	generationCtx, cancelGeneration := context.WithCancelCause(deadlineCtx)
	defer cancelGeneration(context.Canceled)
	if !s.generationGuard.acquire(
		novelIDInt,
		generationID,
		generationCtx,
		cancelGeneration,
	) {
		http.Error(w, "该小说正在生成，请等待当前任务完成后再试", http.StatusConflict)
		return
	}
	leaseOwnedByHandler := true
	defer func() {
		if leaseOwnedByHandler {
			s.generationGuard.release(novelIDInt, generationID)
		}
	}()
	if !s.modelCapacity.tryAcquire() {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "模型正在处理其他任务，请稍后再试", http.StatusTooManyRequests)
		return
	}
	capacityOwnedByHandler := true
	defer func() {
		if capacityOwnedByHandler {
			s.modelCapacity.release()
		}
	}()

	if err := http.NewResponseController(w).SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		log.Printf("[Generation] clear write deadline failed: generation_id=%s novel_id=%s error=%v", generationID, novelID, err)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	if _, ok := w.(http.Flusher); !ok {
		http.Error(w, "Streaming unsupported!", http.StatusInternalServerError)
		return
	}
	sse := newGenerationSSEWriter(w, http.NewResponseController(w))

	outline := strings.TrimSpace(r.URL.Query().Get("outline"))
	idea := strings.TrimSpace(r.URL.Query().Get("idea"))
	editorNotes := strings.TrimSpace(r.URL.Query().Get("editor_notes"))
	manualContext := strings.TrimSpace(r.URL.Query().Get("manual_context"))
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

	ctx := generationCtx
	finishSync := func(runErr error, finalState *agents.GenerationState) {
		cause := s.generationGuard.finish(novelIDInt, generationID)
		result := classifyGenerationResult(
			generationID,
			cause,
			runErr,
			finalState,
		)
		s.generationGuard.release(novelIDInt, generationID)
		leaseOwnedByHandler = false
		s.modelCapacity.release()
		capacityOwnedByHandler = false
		if r.Context().Err() == nil {
			if terminalErr := sse.terminal(result); terminalErr != nil {
				log.Printf("[Generation] terminal write failed: generation_id=%s novel_id=%s error=%v", generationID, novelID, terminalErr)
			}
		}
	}

	if err := sse.send("start", map[string]string{
		"generation_id": generationID,
		"message":       "生成已开始",
	}); err != nil {
		cancelGeneration(err)
		return
	}

	if s.db != nil && (idea == "" || outline == "" || existingOutline == "") {
		loadCtx, loadCancel := context.WithTimeout(ctx, 5*time.Second)
		row, qErr := s.db.Novel.Query().Where(novel.ID(novelIDInt)).Only(loadCtx)
		loadCancel()
		if qErr == nil && row != nil {
			if idea == "" {
				idea = strings.TrimSpace(row.Idea)
			}
			if outline == "" {
				outline = strings.TrimSpace(row.Outline)
			}
			if existingOutline == "" {
				existingOutline = strings.TrimSpace(row.Outline)
			}
		}
	}

	if outline == "" && idea == "" && existingOutline == "" {
		finishSync(
			errors.New("Missing outline and idea (no saved outline found)"),
			nil,
		)
		return
	}

	persist := true
	if persistStr == "0" || persistStr == "false" || persistStr == "no" {
		persist = false
	}

	var chapterTarget *generationChapterTarget
	if persist {
		if s.chapterStore == nil {
			finishSync(errors.New("database not configured"), nil)
			return
		}
		chapterIDInt := 0
		if strings.TrimSpace(chapterIDStr) != "" {
			chapterIDInt, err = parseIntParam(chapterIDStr)
			if err != nil {
				finishSync(err, nil)
				return
			}
		}
		prepareCtx, prepareCancel := context.WithTimeout(ctx, 10*time.Second)
		chapterTarget, err = s.chapterStore.Prepare(
			prepareCtx,
			novelIDInt,
			chapterIDInt,
			chapterIndex,
		)
		prepareCancel()
		if err != nil {
			finishSync(err, nil)
			return
		}
	}

	streamChan := make(chan agents.GenerationStreamEvent)
	streamSink := func(streamCtx context.Context, event agents.GenerationStreamEvent) error {
		if event.Type != agents.GenerationStreamEventToken &&
			event.Type != agents.GenerationStreamEventRetry {
			cancelGeneration(errGenerationProtocol)
			return errGenerationProtocol
		}
		select {
		case streamChan <- event:
			return nil
		case <-streamCtx.Done():
			return streamCtx.Err()
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	chapterID := ""
	chapterIndexForGeneration := chapterIndex
	if chapterTarget != nil {
		chapterID = strconv.Itoa(chapterTarget.ID)
		chapterIndexForGeneration = chapterTarget.Order
	}
	state := &agents.GenerationState{
		GenerationID:    generationID,
		NovelID:         novelID,
		ChapterID:       chapterID,
		ChapterIndex:    chapterIndexForGeneration,
		Idea:            idea,
		FullOutline:     outline,
		EditorNotes:     editorNotes,
		ManualContext:   manualContext,
		ExistingOutline: existingOutline,
		OutlineStart:    outlineStart,
		OutlineEnd:      outlineEnd,
		PreviousContinuity: func() agents.ContinuityPacket {
			if chapterTarget == nil {
				return agents.ContinuityPacket{}
			}
			return chapterTarget.PreviousContinuity
		}(),
	}

	prepared, prepErr := s.engine.PrepareContext(ctx, state)
	if prepErr != nil {
		finishSync(prepErr, nil)
		return
	}
	if prepared == nil {
		finishSync(errors.New("context preparation returned no state"), nil)
		return
	}
	prepared.GenerationID = generationID
	prepared.StreamSink = streamSink
	prepared.NovelID = novelID

	meta := map[string]interface{}{
		"type":                 "context_meta",
		"generation_id":        prepared.GenerationID,
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
	if err := sse.send("context_meta", meta); err != nil {
		cancelGeneration(err)
		return
	}

	resultChan := make(chan generationResult, 1)
	leaseOwnedByHandler = false
	capacityOwnedByHandler = false
	go func() {
		finalState, runErr := s.engine.RunChapterGeneration(ctx, prepared)
		if runErr == nil && persist && finalState != nil {
			finalState.ChapterID = strconv.Itoa(chapterTarget.ID)
			finalState.NovelID = novelID
			finalState, runErr = s.engine.ExtractContinuity(ctx, finalState)
		}
		cause := s.generationGuard.finish(novelIDInt, generationID)
		result := classifyGenerationResult(
			generationID,
			cause,
			runErr,
			finalState,
		)

		if result.Status == generationStatusSuccess && persist {
			postprocessCtx, cancelPostprocess := context.WithDeadline(
				s.lifecycleCtx,
				generationDeadline,
			)
			defer cancelPostprocess()
			saveCtx, saveCancel := context.WithTimeout(postprocessCtx, 10*time.Second)
			saveErr := s.chapterStore.Save(saveCtx, chapterTarget, finalState)
			saveCancel()
			if saveErr != nil {
				message := fmt.Sprintf("save generated chapter: %v", saveErr)
				if errors.Is(saveErr, errGenerationChapterChanged) {
					message = "章节在生成期间已被修改，未覆盖现有内容"
				}
				result = generationResult{
					GenerationID: generationID,
					Status:       generationStatusError,
					Message:      message,
				}
			} else {
				if publishErr := s.engine.PublishChapterGenerated(
					postprocessCtx,
					finalState,
				); publishErr != nil {
					log.Printf(
						"[Generation] 记忆处理失败: generation_id=%s novel_id=%s chapter_id=%s error=%v",
						generationID,
						novelID,
						finalState.ChapterID,
						publishErr,
					)
				}
			}
		}

		s.generationGuard.release(novelIDInt, generationID)
		s.modelCapacity.release()
		resultChan <- result
	}()

	streamEvents := (<-chan agents.GenerationStreamEvent)(streamChan)
	protocolFailed := false
	for {
		select {
		case <-r.Context().Done():
			return
		case result := <-resultChan:
			if r.Context().Err() != nil {
				return
			}
			if protocolFailed {
				result = generationResult{
					GenerationID: generationID,
					Status:       generationStatusError,
					Message:      errGenerationProtocol.Error(),
				}
			}
			if terminalErr := sse.terminal(result); terminalErr != nil {
				log.Printf("[Generation] terminal write failed: generation_id=%s novel_id=%s error=%v", generationID, novelID, terminalErr)
				cancelGeneration(terminalErr)
			}
			return
		case streamEvent := <-streamEvents:
			var sendErr error
			switch streamEvent.Type {
			case agents.GenerationStreamEventRetry:
				sendErr = sse.send("retry", map[string]interface{}{
					"retry_count": streamEvent.RetryCount,
					"critique":    streamEvent.Critique,
				})
			case agents.GenerationStreamEventToken:
				sendErr = sse.send("token", map[string]string{
					"token": streamEvent.Token,
				})
			default:
				protocolFailed = true
				streamEvents = nil
				cancelGeneration(errGenerationProtocol)
				continue
			}
			if sendErr != nil {
				cancelGeneration(sendErr)
				return
			}
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

	if strings.TrimSpace(novelID) == "" {
		http.Error(w, "Missing novel_id and all of outline/idea/existing_outline", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.config.GenerationTimeout)
	defer cancel()
	if !s.modelCapacity.tryAcquire() {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "模型正在处理其他任务，请稍后再试", http.StatusTooManyRequests)
		return
	}
	defer s.modelCapacity.release()
	if err := http.NewResponseController(w).SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		log.Printf("[Preview] clear write deadline failed: novel_id=%s error=%v", novelID, err)
	}
	novelIDInt, err := parseIntParam(novelID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if s.db != nil && (idea == "" || outline == "" || existingOutline == "") {
		loadCtx, loadCancel := context.WithTimeout(ctx, 5*time.Second)
		row, qErr := s.db.Novel.Query().Where(novel.ID(novelIDInt)).Only(loadCtx)
		loadCancel()
		if qErr == nil && row != nil {
			if idea == "" {
				idea = strings.TrimSpace(row.Idea)
			}
			if outline == "" {
				outline = strings.TrimSpace(row.Outline)
			}
			if existingOutline == "" {
				existingOutline = strings.TrimSpace(row.Outline)
			}
		}
	}

	if outline == "" && idea == "" && existingOutline == "" {
		http.Error(w, "Missing outline and idea (no saved outline found)", http.StatusBadRequest)
		return
	}

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
