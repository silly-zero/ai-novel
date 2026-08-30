package agents

import (
	"context"
	"strings"
	"testing"

	domain "github.com/ai-novel/studio/internal/domain/novel"
)

func TestValidateLedgerEvidenceRequiresExactDraftSubstring(t *testing.T) {
	draft := "林云推开石门，停在密室入口。"
	if err := validateLedgerEvidence("state_evidence", "  林云推开石门  ", draft, true); err != nil {
		t.Fatal(err)
	}
	for _, evidence := range []string{"", "林云 推开石门", "林云推开木门"} {
		if err := validateLedgerEvidence("state_evidence", evidence, draft, true); err == nil || !strings.Contains(err.Error(), "state_evidence") {
			t.Fatalf("evidence %q error = %v", evidence, err)
		}
	}
	longEvidence := strings.Repeat("证", maxLedgerEvidenceRunes+1)
	if err := validateLedgerEvidence("state_evidence", longEvidence, longEvidence, true); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("long evidence error = %v", err)
	}
	if err := validateLedgerEvidence("static_evidence", "", draft, false); err != nil {
		t.Fatal(err)
	}
}

func TestCharacterEvidenceRepairWritesOnce(t *testing.T) {
	repo := &characterRepositoryFake{}
	llm := &queuedStructuredLLM{responses: []string{
		`{"characters":[{"name":"林云","current_status":"停在密室入口","identity_evidence":"林云","state_evidence":"不存在的句子"}],"relationships":[]}`,
		`{"characters":[{"name":"林云","current_status":"停在密室入口","identity_evidence":"林云","state_evidence":"林云停在密室入口"}],"relationships":[]}`,
	}}
	state := &GenerationState{GenerationID: "g", NovelID: "7", ChapterID: "11", ChapterIndex: 4, Draft: "林云停在密室入口。"}
	if _, err := NewCharacterAgent(llm, repo).Run(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	if llm.calls != 2 || repo.saveCharacterCalls != 1 {
		t.Fatalf("calls=%d saves=%d", llm.calls, repo.saveCharacterCalls)
	}
	if len(llm.systems) != 2 || !strings.Contains(llm.systems[1], "格式或校验错误") || !strings.Contains(llm.systems[1], "全部业务规则") {
		t.Fatalf("repair system prompt = %#v", llm.systems)
	}
	if len(llm.users) != 2 ||
		!strings.Contains(llm.users[1], "完整替代 JSON") ||
		!strings.Contains(llm.users[1], "category=structured_response_invalid") ||
		!strings.Contains(llm.users[1], "<previous_response>") {
		t.Fatalf("repair user prompt = %#v", llm.users)
	}
}

func TestCharacterEvidenceFailureDoesNotPersist(t *testing.T) {
	repo := &characterRepositoryFake{}
	response := `{"characters":[{"name":"林云","current_status":"停在密室入口","identity_evidence":"林云","state_evidence":"不存在"}],"relationships":[]}`
	llm := &queuedStructuredLLM{responses: []string{response, response}}
	state := &GenerationState{GenerationID: "g", NovelID: "7", ChapterID: "11", ChapterIndex: 4, Draft: "林云停在密室入口。"}
	if _, err := NewCharacterAgent(llm, repo).Run(context.Background(), state); err == nil {
		t.Fatal("invalid evidence was accepted")
	}
	if repo.saveCharacterCalls != 0 || len(repo.savedChanges) != 0 {
		t.Fatalf("persisted character=%d relationships=%#v", repo.saveCharacterCalls, repo.savedChanges)
	}
}

func TestNewLedgerEntitiesRequireStaticEvidence(t *testing.T) {
	character := &CharacterExtraction{Characters: []CharacterUpdate{{
		Name: "林云", Gender: "男", CurrentStatus: "站在门口", IdentityEvidence: "林云", StateEvidence: "站在门口",
	}}}
	if err := validateCharacterExtractionForDraft(character, nil, "林云站在门口。"); err == nil || !strings.Contains(err.Error(), "static_evidence") {
		t.Fatalf("character error=%v", err)
	}
	world := []WorldSettingUpdate{{
		Name: "青云山", Category: "地理", Description: "宗门", CurrentState: "山门封闭", IdentityEvidence: "青云山", StateEvidence: "山门封闭",
	}}
	if err := validateWorldSettingUpdatesForDraft(nil, "青云山山门封闭。")(&world); err == nil || !strings.Contains(err.Error(), "static_evidence") {
		t.Fatalf("world error=%v", err)
	}
}

func TestRelationshipRemoveEvidenceMustBeExactDraftSubstring(t *testing.T) {
	for _, evidence := range []string{"", "他们不再是朋友"} {
		extracted := &CharacterExtraction{Relationships: []RelationshipUpdate{{
			Source: "林云", Target: "苏青", RelationType: "盟友", Operation: domain.RelationshipOperationRemove, Evidence: evidence,
		}}}
		err := validateCharacterExtractionForDraft(extracted, nil, "林云与苏青解除盟友关系。")
		if err == nil || !strings.Contains(err.Error(), "relationships[0].evidence") {
			t.Fatalf("evidence=%q error=%v", evidence, err)
		}
	}
}

func TestRelationshipEvidenceMustBeExactDraftSubstring(t *testing.T) {
	for _, evidence := range []string{"", "两人成为朋友"} {
		extracted := &CharacterExtraction{Relationships: []RelationshipUpdate{{
			Source: "林云", Target: "苏青", RelationType: "盟友", Evidence: evidence,
		}}}
		err := validateCharacterExtractionForDraft(extracted, nil, "林云与苏青结为盟友。")
		if err == nil || !strings.Contains(err.Error(), "relationships[0].evidence") {
			t.Fatalf("evidence=%q error=%v", evidence, err)
		}
	}
}

func TestWorldEvidenceRepairAndFailurePersistence(t *testing.T) {
	draft := "青云山在本章结束时山门封闭。"
	valid := `[{"category":"地理","name":"青云山","description":"群山中的宗门","current_state":"山门封闭","identity_evidence":"青云山","static_evidence":"青云山","state_evidence":"山门封闭"}]`
	invalid := `[{"category":"地理","name":"青云山","description":"群山中的宗门","current_state":"山门封闭","identity_evidence":"青云山","static_evidence":"青云山","state_evidence":"不存在"}]`

	repairedRepo := &worldRepositoryFake{}
	repairedLLM := &queuedStructuredLLM{responses: []string{invalid, valid}}
	state := &GenerationState{GenerationID: "g", NovelID: "7", ChapterID: "11", ChapterIndex: 4, Draft: draft}
	if _, err := NewWorldAgent(repairedLLM, repairedRepo).Run(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	if repairedLLM.calls != 2 || repairedRepo.saveCalls != 1 {
		t.Fatalf("calls=%d saves=%d", repairedLLM.calls, repairedRepo.saveCalls)
	}

	failedRepo := &worldRepositoryFake{}
	failedLLM := &queuedStructuredLLM{responses: []string{invalid, invalid}}
	failedState, err := NewWorldAgent(failedLLM, failedRepo).Run(context.Background(), state)
	if err != nil || failedState != state {
		t.Fatalf("world failure was not downgraded: state=%#v err=%v", failedState, err)
	}
	if failedRepo.saveCalls != 0 || failedRepo.savedSetting != nil {
		t.Fatalf("world saves=%d setting=%#v", failedRepo.saveCalls, failedRepo.savedSetting)
	}
}

func TestWorldStructuredFailureDoesNotReplaceExistingChapterState(t *testing.T) {
	repo := &worldRepositoryFake{existing: &domain.WorldSetting{
		NovelID:      "7",
		Category:     "地理",
		Name:         "青云山",
		Description:  "终年云雾环绕的修炼宗门",
		CurrentState: "山门开放",
	}}
	invalid := `[{"category":"地理","name":"青云山","current_state":"山门封闭","identity_evidence":"青云山","state_evidence":"不存在"}]`
	llm := &queuedStructuredLLM{responses: []string{invalid, invalid}}
	state := &GenerationState{GenerationID: "g", NovelID: "7", ChapterID: "11", ChapterIndex: 4, Draft: "青云山山门封闭。"}

	if _, err := NewWorldAgent(llm, repo).Run(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	if repo.saveCalls != 0 {
		t.Fatalf("world state was replaced after structured failure: saves=%d", repo.saveCalls)
	}
}

func TestWorldStaticEvidenceRequiredOnlyForWrittenStaticFields(t *testing.T) {
	existing := []*domain.WorldSetting{{Name: "青云山", Category: "地理", Description: "宗门"}}
	updates := []WorldSettingUpdate{{Name: "青云山", Category: "地理", CurrentState: "山门封闭", IdentityEvidence: "青云山", StateEvidence: "山门封闭"}}
	if err := validateWorldSettingUpdatesForDraft(existing, "青云山山门封闭。")(&updates); err != nil {
		t.Fatal(err)
	}
	existing[0].Description = ""
	updates[0].Description = "宗门"
	if err := validateWorldSettingUpdatesForDraft(existing, "青云山山门封闭。")(&updates); err == nil || !strings.Contains(err.Error(), "static_evidence") {
		t.Fatalf("error=%v", err)
	}
}

func TestCharacterStaticEvidenceRequiredOnlyForWrittenStaticFields(t *testing.T) {
	existing := []*domain.Character{{Name: "林云", Gender: "男", Age: 20, Appearance: "黑衣", Personality: "谨慎", Background: "边城"}}
	extracted := &CharacterExtraction{Characters: []CharacterUpdate{{
		Name: "林云", CurrentStatus: "停在门口", IdentityEvidence: "林云", StateEvidence: "停在门口",
	}}}
	if err := validateCharacterExtractionForDraft(extracted, existing, "林云停在门口。"); err != nil {
		t.Fatal(err)
	}
	existing[0].Background = ""
	extracted.Characters[0].Background = "边城"
	if err := validateCharacterExtractionForDraft(extracted, existing, "林云停在门口。"); err == nil || !strings.Contains(err.Error(), "static_evidence") {
		t.Fatalf("error = %v", err)
	}
}
