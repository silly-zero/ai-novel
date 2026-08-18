package usecases

import (
	"context"
	"testing"

	"github.com/ai-novel/studio/internal/domain/agents"
	"github.com/ai-novel/studio/internal/domain/events"
)

type ledgerStateAgentFake struct {
	state *agents.GenerationState
}

func (a *ledgerStateAgentFake) Run(
	_ context.Context,
	state *agents.GenerationState,
) (*agents.GenerationState, error) {
	copy := *state
	a.state = &copy
	return state, nil
}

func TestLedgerUseCasesPassChapterStateReference(t *testing.T) {
	event := events.ChapterGeneratedEvent{
		GenerationID: "generation-4",
		NovelID:      "7",
		ChapterID:    "11",
		ChapterIndex: 4,
		Content:      "正文",
	}
	tests := []struct {
		name   string
		handle func(context.Context, events.Event) error
		fake   *ledgerStateAgentFake
	}{
		{
			name: "character",
			fake: &ledgerStateAgentFake{},
		},
		{
			name: "world",
			fake: &ledgerStateAgentFake{},
		},
	}
	tests[0].handle = (&CharacterUseCase{agent: tests[0].fake}).HandleChapterGenerated
	tests[1].handle = (&WorldUseCase{agent: tests[1].fake}).HandleChapterGenerated

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.handle(context.Background(), event); err != nil {
				t.Fatal(err)
			}
			state := test.fake.state
			if state == nil || state.GenerationID != event.GenerationID || state.NovelID != event.NovelID ||
				state.ChapterID != event.ChapterID || state.ChapterIndex != event.ChapterIndex || state.Draft != event.Content {
				t.Fatalf("state = %#v", state)
			}
		})
	}
}
