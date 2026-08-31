package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ai-novel/studio/internal/application/workflows"
	"github.com/ai-novel/studio/internal/domain/agents"
)

type continuousSegmentEngine interface {
	RunContinuousSegment(context.Context, *agents.GenerationState) (*agents.GenerationState, error)
}

func (s *Server) HandleGenerateEvent(w http.ResponseWriter, r *http.Request, request GenerateChapterRequest) {
	if s.engine == nil || s.db == nil || s.chapterStore == nil {
		http.Error(w, "event generation requires configured server", http.StatusInternalServerError)
		return
	}
	if request.EventChapterCount == nil || request.ChapterID != nil || !*request.Persist {
		http.Error(w, "event generation requires persist and cannot target one existing chapter", http.StatusBadRequest)
		return
	}
	chapterCount := *request.EventChapterCount
	novelID := *request.NovelID
	startOrder := *request.ChapterIndex
	generationID, err := agents.NewGenerationID()
	if err != nil {
		http.Error(w, "failed to create generation id", http.StatusInternalServerError)
		return
	}
	generationBase, deadlineCancel := context.WithTimeout(r.Context(), s.config.GenerationTimeout)
	defer deadlineCancel()
	generationCtx, cancel := context.WithCancelCause(generationBase)
	defer cancel(context.Canceled)
	if !s.generationGuard.acquire(novelID, generationID, generationCtx, cancel) {
		http.Error(w, "该小说正在生成，请等待当前任务完成后再试", http.StatusConflict)
		return
	}
	defer s.generationGuard.release(novelID, generationID)
	if !s.modelCapacity.tryAcquire() {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "模型正在处理其他任务，请稍后再试", http.StatusTooManyRequests)
		return
	}
	defer s.modelCapacity.release()
	if err := http.NewResponseController(w).SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		http.Error(w, "stream setup failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("Connection", "keep-alive")
	if _, ok := w.(http.Flusher); !ok {
		http.Error(w, "Streaming unsupported!", http.StatusInternalServerError)
		return
	}
	sse := newGenerationSSEWriter(w, http.NewResponseController(w))
	if err := sse.send("start", map[string]string{"generation_id": generationID, "message": "连续情节生成已开始"}); err != nil {
		return
	}

	loadCtx, loadCancel := context.WithTimeout(generationCtx, 5*time.Second)
	novelRow, err := s.db.Novel.Get(loadCtx, novelID)
	loadCancel()
	if err != nil {
		sendGenerationError(sse, generationID, err)
		return
	}
	outline := strings.TrimSpace(request.Outline)
	if outline == "" {
		outline = strings.TrimSpace(novelRow.Outline)
	}
	idea := strings.TrimSpace(request.Idea)
	if idea == "" {
		idea = strings.TrimSpace(novelRow.Idea)
	}
	if outline == "" && idea == "" {
		sendGenerationError(sse, generationID, errors.New("missing outline and idea"))
		return
	}

	targets := make([]*generationChapterTarget, chapterCount)
	for index := range targets {
		prepareCtx, prepareCancel := context.WithTimeout(generationCtx, 10*time.Second)
		targets[index], err = s.chapterStore.Prepare(prepareCtx, novelID, 0, startOrder+index)
		prepareCancel()
		if err != nil {
			sendGenerationError(sse, generationID, err)
			return
		}
	}

	streamChan := make(chan agents.GenerationStreamEvent)
	previousContinuity := agents.ContinuityPacket{}
	chapters := make([]string, 0, chapterCount)
	var preparedBase *agents.GenerationState
	for index := range targets {
		segmentState := &agents.GenerationState{
			GenerationID:        generationID,
			NovelID:             strconv.Itoa(novelID),
			ChapterID:           strconv.Itoa(targets[index].ID),
			ChapterIndex:        targets[index].Order,
			Idea:                idea,
			FullOutline:         outline,
			ExistingOutline:     outline,
			Outline:             outline,
			EditorNotes:         continuousSegmentNotes(request.EditorNotes, index, chapterCount),
			ManualContext:       request.ManualContext,
			PreviousContinuity:  previousContinuity,
			PreviousChapterTail: lastRunes(strings.Join(chapters, "\n\n"), 500),
			EventChapterCount:   chapterCount,
			EventSegmentIndex:   index + 1,
			StreamSink:          nil,
		}
		if preparedBase == nil {
			preparedBase = segmentState
			preparedBase.Outline = outline
			preparedBase.SceneCard = "围绕同一情节连续推进当前段；从上一段结尾现场直接开始，不重新介绍已出现的人物、地点或能力。"
			preparedBase.Context = "使用小说大纲、上一段结尾和既有正文事实，保持人物位置、动作和因果连续。"
		} else {
			base := *preparedBase
			preparedBase = &base
			preparedBase.GenerationID = generationID
			preparedBase.NovelID = strconv.Itoa(novelID)
			preparedBase.ChapterID = strconv.Itoa(targets[index].ID)
			preparedBase.ChapterIndex = targets[index].Order
			preparedBase.EditorNotes = segmentState.EditorNotes
			preparedBase.ManualContext = request.ManualContext
			preparedBase.PreviousContinuity = previousContinuity
			preparedBase.PreviousChapterTail = lastRunes(strings.Join(chapters, "\n\n"), 500)
			preparedBase.EventChapterCount = chapterCount
			preparedBase.EventSegmentIndex = index + 1
			preparedBase.Draft = ""
			preparedBase.Critique = ""
			preparedBase.RetryCount = 0
			preparedBase.SaveEligible = false
			preparedBase.IsApproved = false
			preparedBase.Continuity = agents.ContinuityPacket{}
			preparedBase.ContinuityExtractionFailed = false
		}
		prepared := preparedBase
		prepared.GenerationID = generationID
		prepared.StreamSink = func(ctx context.Context, event agents.GenerationStreamEvent) error {
			select {
			case streamChan <- event:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		prepared.PreviousContinuity = previousContinuity
		prepared.PreviousChapterTail = lastRunes(strings.Join(chapters, "\n\n"), 500)
		prepared.EventChapterCount = chapterCount
		prepared.EventSegmentIndex = index + 1
		if err := sse.send("context_meta", map[string]any{
			"chapter_index": prepared.ChapterIndex,
			"chapter_count": chapterCount,
			"segment":       index + 1,
		}); err != nil {
			return
		}

		segmentResultChan := make(chan struct {
			state *agents.GenerationState
			err   error
		}, 1)
		go func() {
			state, err := s.engine.(continuousSegmentEngine).RunContinuousSegment(generationCtx, prepared)
			segmentResultChan <- struct {
				state *agents.GenerationState
				err   error
			}{state: state, err: err}
		}()
		var finalState *agents.GenerationState
		var runErr error
		segmentDone := false
		for !segmentDone {
			select {
			case event := <-streamChan:
				if event.Type == agents.GenerationStreamEventToken {
					if err := sse.send("token", map[string]string{"token": event.Token}); err != nil {
						cancel(err)
						return
					}
				} else if event.Type == agents.GenerationStreamEventRetry {
					if err := sse.send("retry", map[string]any{"retry_count": event.RetryCount}); err != nil {
						cancel(err)
						return
					}
				}
			case result := <-segmentResultChan:
				finalState = result.state
				runErr = result.err
				segmentDone = true
			case <-generationCtx.Done():
				return
			}
		}
		canUseDraft := runErr == nil || (finalState != nil && finalState.SaveEligible && errors.Is(runErr, workflows.ErrReviewRetryLimit))
		if !canUseDraft || finalState == nil || strings.TrimSpace(finalState.Draft) == "" {
			if runErr == nil {
				runErr = errors.New("continuous segment generated no usable draft")
			}
			logGenerationDiagnostic(generationID, "chapter_generation", "error", publicErrorCode(runErr), runErr)
			sendGenerationError(sse, generationID, runErr)
			return
		}
		chapters = append(chapters, strings.TrimSpace(finalState.Draft))
		previousContinuity = agents.ContinuityPacket{
			LastBeat:   lastRunes(chapters[len(chapters)-1], 500),
			NextAction: "紧接上一段结尾继续当前情节",
		}
	}

	finalChapters := chapters
	store, ok := s.chapterStore.(eventGenerationChapterStore)
	if !ok {
		sendGenerationError(sse, generationID, errors.New("event persistence is not configured"))
		return
	}
	chapterIDs, saveErr := store.SaveEvent(generationCtx, targets, generationID, finalChapters)
	if saveErr != nil {
		sendGenerationError(sse, generationID, saveErr)
		return
	}
	ids := make([]string, len(chapterIDs))
	for index, chapterID := range chapterIDs {
		ids[index] = strconv.Itoa(chapterID)
		chapterState := &agents.GenerationState{
			GenerationID:               generationID,
			NovelID:                    strconv.Itoa(novelID),
			ChapterID:                  ids[index],
			ChapterIndex:               targets[index].Order,
			Draft:                      finalChapters[index],
			ContinuityExtractionFailed: true,
		}
		publishErr := s.engine.PublishChapterGenerated(generationCtx, chapterState)
		statusCtx, statusCancel := context.WithTimeout(s.lifecycleCtx, 10*time.Second)
		derivedErr := s.finalizeDerivedAfterPublish(statusCtx, chapterID, generationID, publishErr)
		statusCancel()
		if derivedErr != nil {
			logGenerationDiagnostic(generationID, "derived_processing", "error", "derived_processing_failed", derivedErr)
		}
	}
	if err := sse.terminal(generationResult{
		GenerationID: generationID,
		Status:       generationStatusSuccess,
		Message:      fmt.Sprintf("连续情节已拆分为%d章", len(ids)),
		ChapterID:    ids[0],
		ChapterIDs:   ids,
		Persisted:    true,
	}); err != nil {
		logGenerationDiagnostic(generationID, "terminal_delivery", "error", "terminal_delivery_failed", nil)
	}
}

func continuousSegmentNotes(notes string, index, count int) string {
	instructions := fmt.Sprintf("连续情节第%d/%d段：承接上一段最后的动作、对话、人物位置和冲突现场，继续同一故事事件。不要重新介绍人物、地点、能力或入口过程；本段在自然转折处结束，不要人为解决整个宏观事件。", index+1, count)
	if value := strings.TrimSpace(notes); value != "" {
		return instructions + "\n" + value
	}
	return instructions
}

func lastRunes(value string, limit int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) > limit {
		runes = runes[len(runes)-limit:]
	}
	return string(runes)
}

func sendGenerationError(sse *generationSSEWriter, generationID string, err error) {
	_ = sse.terminal(generationResult{GenerationID: generationID, Status: generationStatusError, Message: "生成失败，请重试", ErrorCode: publicErrorCode(err)})
}

func publicErrorCode(err error) string {
	if errors.Is(err, context.Canceled) {
		return "generation_cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "generation_timeout"
	}
	if errors.Is(err, errGenerationChapterChanged) {
		return "chapter_changed"
	}
	if errors.Is(err, errGenerationEarlierChapterStale) || errors.Is(err, errGenerationPreviousChapterMissing) {
		return "generation_failed"
	}
	return "generation_failed"
}
