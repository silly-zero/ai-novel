package agents

import (
	"context"
	"strings"
	"testing"

	domain "github.com/ai-novel/studio/internal/domain/novel"
)

func TestCharacterAgentPassesExplicitRelationshipRemove(t *testing.T) {
	repo := &characterRepositoryFake{
		byName: map[string]*domain.Character{
			"林云": {ID: "1", NovelID: "7", Name: "林云"},
			"苏青": {ID: "2", NovelID: "7", Name: "苏青"},
		},
	}
	llm := memoryAgentTestLLM{response: `{"characters":[],"relationships":[{"source":"林云","target":"苏青","relation_type":"盟友","operation":"remove"}]}`}
	state := &GenerationState{GenerationID: "generation", NovelID: "7", ChapterID: "11", ChapterIndex: 4}

	if _, err := NewCharacterAgent(llm, repo).Run(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	if len(repo.savedChanges) != 1 || repo.savedChanges[0].Operation != domain.RelationshipOperationRemove || repo.savedChanges[0].SourceCharacter.ID != "1" || repo.savedChanges[0].TargetCharacter.ID != "2" {
		t.Fatalf("changes = %#v", repo.savedChanges)
	}
}

func TestCharacterAgentInjectsPriorRelationshipsAndClearsEmptyChapterChanges(t *testing.T) {
	capture := &queuedStructuredLLM{responses: []string{`{"characters":[],"relationships":[]}`}}
	repo := &characterRepositoryFake{relationships: []*domain.Relationship{{
		SourceCharacter: &domain.Character{ID: "1", Name: "林云"},
		TargetCharacter: &domain.Character{ID: "2", Name: "苏青"},
		RelationType:    "盟友",
		Description:     "共同调查",
	}}}
	state := &GenerationState{GenerationID: "generation", NovelID: "7", ChapterID: "11", ChapterIndex: 4}

	if _, err := NewCharacterAgent(capture, repo).Run(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	if repo.relationshipBeforeIndex != 4 || len(repo.savedChanges) != 0 {
		t.Fatalf("boundary = %d, changes = %#v", repo.relationshipBeforeIndex, repo.savedChanges)
	}
	if len(capture.users) != 1 || !strings.Contains(capture.users[0], "林云 --(盟友)--> 苏青：共同调查") {
		t.Fatalf("prompt = %#v", capture.users)
	}
}
