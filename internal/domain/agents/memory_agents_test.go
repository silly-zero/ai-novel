package agents

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	domain "github.com/ai-novel/studio/internal/domain/novel"
)

type memoryAgentTestLLM struct {
	response string
}

func (f memoryAgentTestLLM) Generate(context.Context, string, string) (string, error) {
	return f.response, nil
}

func (f memoryAgentTestLLM) StreamGenerate(
	context.Context,
	string,
	string,
	func(string) error,
) error {
	return nil
}

type characterRepositoryFake struct {
	listErr                 error
	saveCharacterErr        error
	saveRelationErr         error
	existing                *domain.Character
	byName                  map[string]*domain.Character
	relationships           []*domain.Relationship
	savedCharacter          *domain.Character
	savedRef                domain.ChapterStateRef
	saveCharacterCalls      int
	lastCharacterName       string
	listBeforeIndex         int
	savedRelationship       *domain.Relationship
	savedChanges            []domain.RelationshipChange
	relationshipBeforeIndex int
}

func (*characterRepositoryFake) GetCharacter(context.Context, string) (*domain.Character, error) {
	return nil, errors.New("not found")
}

func (r *characterRepositoryFake) FindByName(_ context.Context, _ string, name string) (*domain.Character, error) {
	if character := r.byName[name]; character != nil {
		copy := *character
		return &copy, nil
	}
	if r.existing == nil {
		return nil, errors.New("not found")
	}
	copy := *r.existing
	return &copy, nil
}

func (r *characterRepositoryFake) ListCharacters(context.Context, string) ([]*domain.Character, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	if r.existing == nil {
		return nil, nil
	}
	return []*domain.Character{r.existing}, nil
}

func (r *characterRepositoryFake) ListCharactersBeforeChapter(
	ctx context.Context,
	novelID string,
	chapterIndex int,
) ([]*domain.Character, error) {
	r.listBeforeIndex = chapterIndex
	return r.ListCharacters(ctx, novelID)
}

func (r *characterRepositoryFake) ReplaceChapterCharacters(
	_ context.Context,
	ref domain.ChapterStateRef,
	characters []*domain.Character,
) ([]*domain.Character, error) {
	if r.saveCharacterErr != nil {
		return nil, r.saveCharacterErr
	}
	r.saveCharacterCalls++
	r.savedRef = ref
	result := make([]*domain.Character, len(characters))
	for index, input := range characters {
		canonical := *input
		canonical.ID = fmt.Sprintf("%d", index+1)
		canonical.NovelID = ref.NovelID
		if r.existing != nil && r.existing.Name == input.Name {
			canonical.ID = "1"
			canonical.NovelID = r.existing.NovelID
			if strings.TrimSpace(r.existing.Gender) != "" {
				canonical.Gender = r.existing.Gender
			}
			if r.existing.Age != 0 {
				canonical.Age = r.existing.Age
			}
			if strings.TrimSpace(r.existing.Appearance) != "" {
				canonical.Appearance = r.existing.Appearance
			}
			if strings.TrimSpace(r.existing.Personality) != "" {
				canonical.Personality = r.existing.Personality
			}
			if strings.TrimSpace(r.existing.Background) != "" {
				canonical.Background = r.existing.Background
			}
		}
		copy := canonical
		result[index] = &copy
		if index == 0 {
			r.savedCharacter = &copy
		}
	}
	return result, nil
}

func (r *characterRepositoryFake) ReplaceChapterRelationships(
	_ context.Context,
	_ domain.ChapterStateRef,
	changes []domain.RelationshipChange,
) ([]*domain.Relationship, error) {
	r.savedChanges = append([]domain.RelationshipChange(nil), changes...)
	if r.saveRelationErr != nil {
		return nil, r.saveRelationErr
	}
	result := make([]*domain.Relationship, 0, len(changes))
	for _, change := range changes {
		if change.Operation == domain.RelationshipOperationRemove {
			continue
		}
		relationship := &domain.Relationship{
			ID:              "1",
			SourceCharacter: change.SourceCharacter,
			TargetCharacter: change.TargetCharacter,
			RelationType:    change.RelationType,
			Description:     change.Description,
		}
		result = append(result, relationship)
		copy := *relationship
		r.savedRelationship = &copy
	}
	return result, nil
}

func (r *characterRepositoryFake) ListRelationshipsBeforeChapter(
	_ context.Context,
	_ string,
	chapterIndex int,
) ([]*domain.Relationship, error) {
	r.relationshipBeforeIndex = chapterIndex
	return r.relationships, nil
}

func (r *characterRepositoryFake) ListRelationships(context.Context, string) ([]*domain.Relationship, error) {
	return r.relationships, nil
}

type worldRepositoryFake struct {
	listErr         error
	saveErr         error
	existing        *domain.WorldSetting
	savedSetting    *domain.WorldSetting
	savedRef        domain.ChapterStateRef
	saveCalls       int
	listBeforeIndex int
}

func (r *worldRepositoryFake) FindByName(context.Context, string, string) (*domain.WorldSetting, error) {
	if r.existing == nil {
		return nil, errors.New("not found")
	}
	copy := *r.existing
	return &copy, nil
}

func (*worldRepositoryFake) ListByCategory(context.Context, string, string) ([]*domain.WorldSetting, error) {
	return nil, nil
}

func (r *worldRepositoryFake) ListAll(context.Context, string) ([]*domain.WorldSetting, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	if r.existing == nil {
		return nil, nil
	}
	return []*domain.WorldSetting{r.existing}, nil
}

func (r *worldRepositoryFake) ListWorldSettingsBeforeChapter(
	ctx context.Context,
	novelID string,
	chapterIndex int,
) ([]*domain.WorldSetting, error) {
	r.listBeforeIndex = chapterIndex
	return r.ListAll(ctx, novelID)
}

func (r *worldRepositoryFake) ReplaceChapterWorldSettings(
	_ context.Context,
	ref domain.ChapterStateRef,
	settings []*domain.WorldSetting,
) ([]*domain.WorldSetting, error) {
	if r.saveErr != nil {
		return nil, r.saveErr
	}
	r.saveCalls++
	r.savedRef = ref
	result := make([]*domain.WorldSetting, len(settings))
	for index, input := range settings {
		canonical := *input
		canonical.ID = fmt.Sprintf("%d", index+1)
		canonical.NovelID = ref.NovelID
		if r.existing != nil && r.existing.Name == input.Name {
			canonical.ID = "1"
			canonical.NovelID = r.existing.NovelID
			if strings.TrimSpace(r.existing.Category) != "" {
				canonical.Category = r.existing.Category
			}
			if strings.TrimSpace(r.existing.Description) != "" {
				canonical.Description = r.existing.Description
			}
		}
		copy := canonical
		result[index] = &copy
		if index == 0 {
			r.savedSetting = &copy
		}
	}
	return result, nil
}

func characterEvidenceDraft() string {
	return "青云山 地理 终年云雾环绕的修炼宗门 本章结束时山门封闭并由长老守卫 本章结束时山门封闭 玄天镜 宝物 可映照灵力轨迹的古镜 本章结束时由林云持有 群山中的修炼宗门 山门已经封闭 林云 男 20 黑衣 谨慎 边城出身 旧外貌 新外貌 新性格 新背景 坚定 旧背景 留在青云山等待消息 寻找苏青 苏青 与林云会合 本章结束时已进入密室 本章结束时离开边城"
}

func TestCharacterAgentReturnsRepositoryErrors(t *testing.T) {
	listErr := errors.New("list failed")
	agent := NewCharacterAgent(memoryAgentTestLLM{}, &characterRepositoryFake{listErr: listErr})
	if _, err := agent.Run(context.Background(), &GenerationState{GenerationID: "generation", NovelID: "7", ChapterID: "11", ChapterIndex: 4, Draft: characterEvidenceDraft()}); !errors.Is(err, listErr) {
		t.Fatalf("list error = %v, want %v", err, listErr)
	}

	saveErr := errors.New("save character failed")
	agent = NewCharacterAgent(memoryAgentTestLLM{response: `{"characters":[{"name":"林云","current_status":"留在青云山等待消息","identity_evidence":"林云","state_evidence":"留在青云山等待消息"}]}`}, &characterRepositoryFake{saveCharacterErr: saveErr})
	if _, err := agent.Run(context.Background(), &GenerationState{GenerationID: "generation", NovelID: "7", ChapterID: "11", ChapterIndex: 4, Draft: characterEvidenceDraft()}); !errors.Is(err, saveErr) {
		t.Fatalf("save character error = %v, want %v", err, saveErr)
	}

	relationErr := errors.New("save relationship failed")
	relationRepo := &characterRepositoryFake{saveRelationErr: relationErr}
	agent = NewCharacterAgent(memoryAgentTestLLM{response: `{"characters":[{"name":"林云","current_status":"寻找苏青","identity_evidence":"林云","state_evidence":"寻找苏青"},{"name":"苏青","current_status":"与林云会合","identity_evidence":"苏青","state_evidence":"与林云会合"}],"relationships":[{"source":"林云","target":"苏青","relation_type":"盟友","evidence":"与林云会合"}]}`}, relationRepo)
	if _, err := agent.Run(context.Background(), &GenerationState{GenerationID: "generation", NovelID: "7", ChapterID: "11", ChapterIndex: 4, Draft: characterEvidenceDraft()}); !errors.Is(err, relationErr) {
		t.Fatalf("save relationship error = %v, want %v", err, relationErr)
	}
	if relationRepo.saveCharacterCalls != 1 || relationRepo.savedCharacter == nil {
		t.Fatalf("character state replacement was not committed before relationship failure: calls=%d character=%#v", relationRepo.saveCharacterCalls, relationRepo.savedCharacter)
	}
}

func TestCharacterAgentUsesCanonicalRelationshipEndpoints(t *testing.T) {
	repo := &characterRepositoryFake{}
	llm := memoryAgentTestLLM{response: `{"characters":[{"name":"林云","current_status":"寻找苏青","identity_evidence":"林云","state_evidence":"寻找苏青"},{"name":"苏青","current_status":"与林云会合","identity_evidence":"苏青","state_evidence":"与林云会合"}],"relationships":[{"source":"林云","target":"苏青","relation_type":"盟友","evidence":"与林云会合"}]}`}
	state := &GenerationState{GenerationID: "generation", NovelID: "7", ChapterID: "11", ChapterIndex: 4, Draft: characterEvidenceDraft()}

	if _, err := NewCharacterAgent(llm, repo).Run(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	if repo.savedRelationship == nil || repo.savedRelationship.SourceCharacter.ID != "1" || repo.savedRelationship.TargetCharacter.ID != "2" {
		t.Fatalf("relationship = %#v", repo.savedRelationship)
	}
	if len(repo.savedChanges) != 1 || repo.savedChanges[0].Operation != domain.RelationshipOperationUpsert {
		t.Fatalf("relationship changes = %#v", repo.savedChanges)
	}
}

func TestCharacterAgentReturnsRelationshipResolutionError(t *testing.T) {
	repo := &characterRepositoryFake{}
	llm := memoryAgentTestLLM{response: `{"characters":[],"relationships":[{"source":"林云","target":"苏青","relation_type":"盟友","evidence":"与林云会合"}]}`}
	state := &GenerationState{GenerationID: "generation", NovelID: "7", ChapterID: "11", ChapterIndex: 4, Draft: characterEvidenceDraft()}

	if _, err := NewCharacterAgent(llm, repo).Run(context.Background(), state); err == nil || !strings.Contains(err.Error(), "resolve relationship character") {
		t.Fatalf("error = %v", err)
	}
	if repo.saveCharacterCalls != 0 {
		t.Fatalf("character states committed before relationship resolution: %d", repo.saveCharacterCalls)
	}
}

func TestCharacterAgentPreservesStaticLedgerAndReplacesCurrentStatus(t *testing.T) {
	repo := &characterRepositoryFake{existing: &domain.Character{
		NovelID:       "7",
		Name:          "林云",
		Gender:        "男",
		Age:           20,
		Appearance:    "旧外貌",
		Personality:   "坚定",
		Background:    "旧背景",
		CurrentStatus: "上一章状态",
	}}
	llm := memoryAgentTestLLM{response: `{"characters":[{"name":"林云","gender":"女","age":30,"appearance":"新外貌","personality":"新性格","background":"新背景","current_status":"本章结束时已进入密室","identity_evidence":"林云","state_evidence":"本章结束时已进入密室"}]}`}

	if _, err := NewCharacterAgent(llm, repo).Run(context.Background(), &GenerationState{GenerationID: "generation", NovelID: "7", ChapterID: "11", ChapterIndex: 4, Draft: characterEvidenceDraft()}); err != nil {
		t.Fatal(err)
	}
	if repo.savedCharacter == nil {
		t.Fatal("character was not saved")
	}
	got := repo.savedCharacter
	if got.Gender != "男" || got.Age != 20 || got.Appearance != "旧外貌" || got.Personality != "坚定" || got.Background != "旧背景" {
		t.Fatalf("static fields were overwritten: %#v", got)
	}
	if got.CurrentStatus != "本章结束时已进入密室" {
		t.Fatalf("CurrentStatus = %q", got.CurrentStatus)
	}
	if repo.listBeforeIndex != 4 || repo.savedRef.ChapterID != "11" || repo.savedRef.ChapterIndex != 4 || repo.savedRef.GenerationID != "generation" {
		t.Fatalf("chapter state boundary/ref = %d / %#v", repo.listBeforeIndex, repo.savedRef)
	}
}

func TestCharacterAgentFillsMissingStaticFields(t *testing.T) {
	repo := &characterRepositoryFake{existing: &domain.Character{
		NovelID:       "7",
		Name:          "林云",
		Gender:        "   ",
		Appearance:    "   ",
		Personality:   "   ",
		Background:    "   ",
		CurrentStatus: "上一章状态",
	}}
	llm := memoryAgentTestLLM{response: `{"characters":[{"name":"林云","gender":"男","age":20,"appearance":"黑衣","personality":"谨慎","background":"边城出身","current_status":"本章结束时离开边城","identity_evidence":"林云","static_evidence":"男 20 黑衣 谨慎 边城出身","state_evidence":"本章结束时离开边城"}]}`}

	if _, err := NewCharacterAgent(llm, repo).Run(context.Background(), &GenerationState{GenerationID: "generation", NovelID: "7", ChapterID: "11", ChapterIndex: 4, Draft: characterEvidenceDraft()}); err != nil {
		t.Fatal(err)
	}
	got := repo.savedCharacter
	if got == nil || got.Gender != "男" || got.Age != 20 || got.Appearance != "黑衣" || got.Personality != "谨慎" || got.Background != "边城出身" {
		t.Fatalf("missing static fields were not filled: %#v", got)
	}
	if got.CurrentStatus != "本章结束时离开边城" {
		t.Fatalf("CurrentStatus = %q", got.CurrentStatus)
	}
}

func TestWorldAgentPreservesStaticLedgerAndReplacesCurrentState(t *testing.T) {
	repo := &worldRepositoryFake{existing: &domain.WorldSetting{
		NovelID:      "7",
		Category:     "地理",
		Name:         "青云山",
		Description:  "终年云雾环绕的修炼宗门",
		CurrentState: "山门开放",
	}}
	llm := memoryAgentTestLLM{response: `[{"category":"势力","name":"青云山","description":"被改写的说明","current_state":"本章结束时山门封闭并由长老守卫","identity_evidence":"青云山","state_evidence":"本章结束时山门封闭并由长老守卫"}]`}

	if _, err := NewWorldAgent(llm, repo).Run(context.Background(), &GenerationState{GenerationID: "generation", NovelID: "7", ChapterID: "11", ChapterIndex: 4, Draft: characterEvidenceDraft()}); err != nil {
		t.Fatal(err)
	}
	got := repo.savedSetting
	if got == nil {
		t.Fatal("world setting was not saved")
	}
	if got.Category != "地理" || got.Description != "终年云雾环绕的修炼宗门" {
		t.Fatalf("static fields were overwritten: %#v", got)
	}
	if got.CurrentState != "本章结束时山门封闭并由长老守卫" {
		t.Fatalf("CurrentState = %q", got.CurrentState)
	}
	if repo.listBeforeIndex != 4 || repo.savedRef.ChapterID != "11" || repo.savedRef.ChapterIndex != 4 || repo.savedRef.GenerationID != "generation" {
		t.Fatalf("chapter state boundary/ref = %d / %#v", repo.listBeforeIndex, repo.savedRef)
	}
}

func TestWorldAgentFillsMissingStaticFields(t *testing.T) {
	repo := &worldRepositoryFake{existing: &domain.WorldSetting{
		NovelID:      "7",
		Category:     "   ",
		Name:         "青云山",
		Description:  "   ",
		CurrentState: "旧状态",
	}}
	llm := memoryAgentTestLLM{response: `[{"category":"地理","name":"青云山","description":"终年云雾环绕的修炼宗门","current_state":"本章结束时山门封闭","identity_evidence":"青云山","static_evidence":"地理 终年云雾环绕的修炼宗门","state_evidence":"本章结束时山门封闭"}]`}

	if _, err := NewWorldAgent(llm, repo).Run(context.Background(), &GenerationState{GenerationID: "generation", NovelID: "7", ChapterID: "11", ChapterIndex: 4, Draft: characterEvidenceDraft()}); err != nil {
		t.Fatal(err)
	}
	got := repo.savedSetting
	if got == nil || got.Category != "地理" || got.Description != "终年云雾环绕的修炼宗门" {
		t.Fatalf("missing static fields were not filled: %#v", got)
	}
	if got.CurrentState != "本章结束时山门封闭" {
		t.Fatalf("CurrentState = %q", got.CurrentState)
	}
}

func TestWorldAgentCreatesCompleteLedgerEntry(t *testing.T) {
	repo := &worldRepositoryFake{}
	llm := memoryAgentTestLLM{response: `[{"category":"宝物","name":"玄天镜","description":"可映照灵力轨迹的古镜","current_state":"本章结束时由林云持有","identity_evidence":"玄天镜","static_evidence":"宝物 可映照灵力轨迹的古镜","state_evidence":"本章结束时由林云持有"}]`}

	if _, err := NewWorldAgent(llm, repo).Run(context.Background(), &GenerationState{GenerationID: "generation", NovelID: "7", ChapterID: "11", ChapterIndex: 4, Draft: characterEvidenceDraft()}); err != nil {
		t.Fatal(err)
	}
	got := repo.savedSetting
	if got == nil || got.NovelID != "7" || got.Category != "宝物" || got.Description == "" || got.CurrentState == "" {
		t.Fatalf("new ledger entry = %#v", got)
	}
}

func TestWorldAgentReturnsRepositoryErrors(t *testing.T) {
	listErr := errors.New("list failed")
	agent := NewWorldAgent(memoryAgentTestLLM{}, &worldRepositoryFake{listErr: listErr})
	if _, err := agent.Run(context.Background(), &GenerationState{GenerationID: "generation", NovelID: "7", ChapterID: "11", ChapterIndex: 4, Draft: characterEvidenceDraft()}); !errors.Is(err, listErr) {
		t.Fatalf("list error = %v, want %v", err, listErr)
	}

	saveErr := errors.New("save setting failed")
	agent = NewWorldAgent(memoryAgentTestLLM{response: `[{"category":"地理","name":"青云山","description":"群山中的修炼宗门","current_state":"山门已经封闭","identity_evidence":"青云山","static_evidence":"群山中的修炼宗门","state_evidence":"山门已经封闭"}]`}, &worldRepositoryFake{saveErr: saveErr})
	if _, err := agent.Run(context.Background(), &GenerationState{GenerationID: "generation", NovelID: "7", ChapterID: "11", ChapterIndex: 4, Draft: characterEvidenceDraft()}); !errors.Is(err, saveErr) {
		t.Fatalf("save setting error = %v, want %v", err, saveErr)
	}
}
