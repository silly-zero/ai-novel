package agents

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ai-novel/studio/internal/domain/memory"
	domain "github.com/ai-novel/studio/internal/domain/novel"
)

type librarianCharacterRepositoryFake struct {
	characters                 []*domain.Character
	relationships              []*domain.Relationship
	listErr                    error
	relationshipErr            error
	listCalls                  int
	findCalls                  int
	relationshipCalls          int
	listNovelIDs               []string
	listChapterIndexes         []int
	relationshipNovelIDs       []string
	relationshipChapterIndexes []int
}

func (*librarianCharacterRepositoryFake) GetCharacter(context.Context, string) (*domain.Character, error) {
	return nil, errors.New("not found")
}

func (r *librarianCharacterRepositoryFake) FindByName(context.Context, string, string) (*domain.Character, error) {
	r.findCalls++
	return nil, errors.New("FindByName must not be called")
}

func (r *librarianCharacterRepositoryFake) ListCharacters(_ context.Context, novelID string) ([]*domain.Character, error) {
	r.listCalls++
	r.listNovelIDs = append(r.listNovelIDs, novelID)
	return r.characters, r.listErr
}

func (r *librarianCharacterRepositoryFake) ListCharactersBeforeChapter(
	ctx context.Context,
	novelID string,
	chapterIndex int,
) ([]*domain.Character, error) {
	r.listChapterIndexes = append(r.listChapterIndexes, chapterIndex)
	return r.ListCharacters(ctx, novelID)
}

func (r *librarianCharacterRepositoryFake) ReplaceChapterCharacters(
	context.Context,
	domain.ChapterStateRef,
	[]*domain.Character,
) ([]*domain.Character, error) {
	return nil, nil
}

func (*librarianCharacterRepositoryFake) ReplaceChapterRelationships(
	context.Context,
	domain.ChapterStateRef,
	[]domain.RelationshipChange,
) ([]*domain.Relationship, error) {
	return nil, nil
}

func (r *librarianCharacterRepositoryFake) ListRelationshipsBeforeChapter(
	_ context.Context,
	novelID string,
	chapterIndex int,
) ([]*domain.Relationship, error) {
	r.relationshipCalls++
	r.relationshipNovelIDs = append(r.relationshipNovelIDs, novelID)
	r.relationshipChapterIndexes = append(r.relationshipChapterIndexes, chapterIndex)
	return r.relationships, r.relationshipErr
}

func (r *librarianCharacterRepositoryFake) ListRelationships(_ context.Context, novelID string) ([]*domain.Relationship, error) {
	r.relationshipCalls++
	r.relationshipNovelIDs = append(r.relationshipNovelIDs, novelID)
	return r.relationships, r.relationshipErr
}

type librarianWorldRepositoryFake struct {
	settings           []*domain.WorldSetting
	listErr            error
	listCalls          int
	findCalls          int
	listNovelIDs       []string
	listChapterIndexes []int
}

func (r *librarianWorldRepositoryFake) FindByName(context.Context, string, string) (*domain.WorldSetting, error) {
	r.findCalls++
	return nil, errors.New("FindByName must not be called")
}

func (*librarianWorldRepositoryFake) ListByCategory(context.Context, string, string) ([]*domain.WorldSetting, error) {
	return nil, nil
}

func (r *librarianWorldRepositoryFake) ListAll(_ context.Context, novelID string) ([]*domain.WorldSetting, error) {
	r.listCalls++
	r.listNovelIDs = append(r.listNovelIDs, novelID)
	return r.settings, r.listErr
}

func (r *librarianWorldRepositoryFake) ListWorldSettingsBeforeChapter(
	ctx context.Context,
	novelID string,
	chapterIndex int,
) ([]*domain.WorldSetting, error) {
	r.listChapterIndexes = append(r.listChapterIndexes, chapterIndex)
	return r.ListAll(ctx, novelID)
}

func (r *librarianWorldRepositoryFake) ReplaceChapterWorldSettings(
	context.Context,
	domain.ChapterStateRef,
	[]*domain.WorldSetting,
) ([]*domain.WorldSetting, error) {
	return nil, nil
}

type librarianLLMFake struct {
	response string
}

func (f librarianLLMFake) Generate(context.Context, string, string) (string, error) {
	return f.response, nil
}

func (f librarianLLMFake) StreamGenerate(context.Context, string, string, func(string) error) error {
	return nil
}

type librarianEmbedderFake struct {
	inputs  [][]string
	vectors [][]float32
	err     error
}

func (f *librarianEmbedderFake) EmbedText(context.Context, string) ([]float32, error) {
	return nil, errors.New("EmbedText must not be called")
}

func (f *librarianEmbedderFake) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	f.inputs = append(f.inputs, append([]string(nil), texts...))
	return f.vectors, f.err
}

type librarianVectorStoreFake struct {
	results [][]memory.SearchResult
	err     error
	calls   []librarianSearchCall
}

type librarianSearchCall struct {
	novelID string
	vector  []float32
	options memory.SearchOptions
}

func (f *librarianVectorStoreFake) Add(context.Context, []*memory.MemoryEntry) error {
	return nil
}

func (f *librarianVectorStoreFake) Search(
	_ context.Context,
	novelID string,
	vector []float32,
	options memory.SearchOptions,
) ([]memory.SearchResult, error) {
	f.calls = append(f.calls, librarianSearchCall{
		novelID: novelID,
		vector:  append([]float32(nil), vector...),
		options: options,
	})
	if f.err != nil {
		return nil, f.err
	}
	return f.results[len(f.calls)-1], nil
}

func librarianTestConfig() LibrarianConfig {
	return LibrarianConfig{
		SearchOptions: memory.SearchOptions{
			CandidateLimit: 100,
			ResultLimit:    4,
			MinSimilarity:  0.55,
		},
		MaxQueries:         2,
		MaxContextMemories: 2,
	}
}

func TestLibrarianBoundsMemorySearchBeforeCurrentChapter(t *testing.T) {
	store := &librarianVectorStoreFake{results: [][]memory.SearchResult{{}}}
	agent := NewLibrarianAgent(
		librarianLLMFake{response: `{"character_names":[],"world_settings":[],"search_queries":[]}`},
		&librarianEmbedderFake{vectors: [][]float32{{1}}},
		store,
		nil,
		nil,
		librarianTestConfig(),
	)
	state := &GenerationState{NovelID: "novel", ChapterIndex: 4, Outline: "本章线索"}

	if _, err := agent.Run(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	if len(store.calls) != 1 || store.calls[0].options.BeforeChapterIndex != 4 {
		t.Fatalf("search calls = %#v, want boundary before chapter 4", store.calls)
	}
}

func TestLibrarianRejectsMissingChapterIndex(t *testing.T) {
	agent := NewLibrarianAgent(
		librarianLLMFake{response: `{"character_names":[],"world_settings":[],"search_queries":["查询"]}`},
		&librarianEmbedderFake{vectors: [][]float32{{1}}},
		&librarianVectorStoreFake{results: [][]memory.SearchResult{{}}},
		nil,
		nil,
		librarianTestConfig(),
	)

	if _, err := agent.Run(context.Background(), &GenerationState{NovelID: "novel"}); err == nil || !strings.Contains(err.Error(), "chapter index must be positive") {
		t.Fatalf("error = %v", err)
	}
}

func TestLibrarianRebuildsExistingContext(t *testing.T) {
	embedder := &librarianEmbedderFake{vectors: [][]float32{{1}}}
	store := &librarianVectorStoreFake{results: [][]memory.SearchResult{{
		{Entry: &memory.MemoryEntry{Content: "新记忆"}, Score: 0.9},
	}}}
	agent := NewLibrarianAgent(
		librarianLLMFake{response: `{"character_names":[],"world_settings":[],"search_queries":["新查询"]}`},
		embedder,
		store,
		nil,
		nil,
		librarianTestConfig(),
	)
	state := &GenerationState{
		NovelID:          "novel",
		ChapterIndex:     4,
		Context:          "旧背景",
		CanonConstraints: []CanonConstraint{{Kind: "old"}},
	}

	result, err := agent.Run(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Context, "旧背景") || !strings.Contains(result.Context, "新记忆") {
		t.Fatalf("Context = %q", result.Context)
	}
	if len(result.CanonConstraints) != 0 {
		t.Fatalf("CanonConstraints = %#v, want empty", result.CanonConstraints)
	}
	if !reflect.DeepEqual(embedder.inputs, [][]string{{"新查询"}}) {
		t.Fatalf("EmbedBatch inputs = %#v", embedder.inputs)
	}
}

func TestLibrarianFallbackClearsDerivedState(t *testing.T) {
	state := &GenerationState{
		ChapterIndex:     4,
		Context:          "旧背景",
		CanonConstraints: []CanonConstraint{{Kind: "old"}},
	}
	result, err := NewLibrarianAgent(nil, nil, nil, nil, nil, LibrarianConfig{}).Run(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if result.Context != "（暂无背景资料，请根据大纲自由发挥）" || result.CanonConstraints != nil {
		t.Fatalf("state = %#v", result)
	}
}

func TestLibrarianPrioritizesDeterministicQuery(t *testing.T) {
	embedder := &librarianEmbedderFake{vectors: [][]float32{{1}}}
	agent := NewLibrarianAgent(
		librarianLLMFake{response: `{"character_names":[],"world_settings":[],"search_queries":["模型泛化查询"]}`},
		embedder,
		&librarianVectorStoreFake{results: [][]memory.SearchResult{{}}},
		nil,
		nil,
		LibrarianConfig{MaxQueries: 1, MaxContextMemories: 2},
	)
	state := &GenerationState{
		NovelID:      "novel",
		ChapterIndex: 4,
		ChapterContract: ChapterContract{
			Goal:       "调查血书",
			MustHappen: []string{"林云进入密室"},
		},
	}

	if _, err := agent.Run(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(embedder.inputs, [][]string{{"调查血书；林云进入密室"}}) {
		t.Fatalf("EmbedBatch inputs = %#v", embedder.inputs)
	}
}

func TestLibrarianUsesLLMQueriesWhenDeterministicQueryIsEmpty(t *testing.T) {
	embedder := &librarianEmbedderFake{vectors: [][]float32{{1}, {2}}}
	agent := NewLibrarianAgent(
		librarianLLMFake{response: `{"character_names":[],"world_settings":[],"search_queries":[" 查询一 ","查询二"]}`},
		embedder,
		&librarianVectorStoreFake{results: [][]memory.SearchResult{{}, {}}},
		nil,
		nil,
		librarianTestConfig(),
	)

	if _, err := agent.Run(context.Background(), &GenerationState{NovelID: "novel", ChapterIndex: 4}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(embedder.inputs, [][]string{{"查询一", "查询二"}}) {
		t.Fatalf("EmbedBatch inputs = %#v", embedder.inputs)
	}
}

func TestLibrarianSkipsVectorRetrievalWithoutQueries(t *testing.T) {
	embedder := &librarianEmbedderFake{err: errors.New("must not be called")}
	store := &librarianVectorStoreFake{err: errors.New("must not be called")}
	agent := NewLibrarianAgent(
		librarianLLMFake{response: `{"character_names":[],"world_settings":[],"search_queries":[]}`},
		embedder,
		store,
		nil,
		nil,
		librarianTestConfig(),
	)
	state := &GenerationState{NovelID: "novel", ChapterIndex: 4, Context: "旧背景"}

	result, err := agent.Run(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if len(embedder.inputs) != 0 || len(store.calls) != 0 {
		t.Fatalf("retrieval called without queries: embed=%#v search=%#v", embedder.inputs, store.calls)
	}
	if result.Context != "" {
		t.Fatalf("Context = %q, want successful empty rebuild", result.Context)
	}
}

func TestDeterministicRetrievalQueryIsRuneBounded(t *testing.T) {
	query := deterministicRetrievalQuery([]string{strings.Repeat("界", deterministicRetrievalQueryMaxRunes+10)})
	if got := len([]rune(query)); got != deterministicRetrievalQueryMaxRunes || !utf8.ValidString(query) {
		t.Fatalf("query rune length = %d, valid UTF-8 = %v", got, utf8.ValidString(query))
	}
}

func TestLibrarianBatchesQueriesAndGloballyRanksMemories(t *testing.T) {
	embedder := &librarianEmbedderFake{vectors: [][]float32{{1, 0}, {0, 1}}}
	store := &librarianVectorStoreFake{results: [][]memory.SearchResult{
		{
			{Entry: &memory.MemoryEntry{ID: "b", Content: " 重复记忆 "}, Score: 0.7},
			{Entry: &memory.MemoryEntry{ID: "c", Content: "第三条"}, Score: 0.8},
		},
		{
			{Entry: &memory.MemoryEntry{ID: "a", Content: "重复记忆"}, Score: 0.95},
			{Entry: &memory.MemoryEntry{ID: "d", Content: "第二条"}, Score: 0.9},
		},
	}}
	agent := NewLibrarianAgent(
		librarianLLMFake{response: `{"character_names":[],"world_settings":[],"search_queries":[" 查询一 ","查询一","查询二","查询三"]}`},
		embedder,
		store,
		nil,
		nil,
		librarianTestConfig(),
	)
	state := &GenerationState{NovelID: "novel-1", ChapterIndex: 4, Outline: "outline"}

	result, err := agent.Run(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(embedder.inputs, [][]string{{"outline", "查询一"}}) {
		t.Fatalf("EmbedBatch inputs = %#v", embedder.inputs)
	}
	if len(store.calls) != 2 {
		t.Fatalf("search calls = %d, want 2", len(store.calls))
	}
	wantOptions := librarianTestConfig().SearchOptions
	wantOptions.BeforeChapterIndex = 4
	for _, call := range store.calls {
		if call.novelID != "novel-1" || call.options != wantOptions {
			t.Fatalf("search call = %#v", call)
		}
	}
	want := "【前情提要与伏笔】\n- 重复记忆\n- 第二条\n"
	if result.Context != want {
		t.Fatalf("Context = %q, want %q", result.Context, want)
	}
	if strings.Count(result.Context, "重复记忆") != 1 || strings.Contains(result.Context, "第三条") {
		t.Fatalf("Context did not deduplicate and cap globally: %q", result.Context)
	}
}

func TestLibrarianOmitsMemorySectionWithoutRelevantResults(t *testing.T) {
	embedder := &librarianEmbedderFake{vectors: [][]float32{{1}}}
	store := &librarianVectorStoreFake{results: [][]memory.SearchResult{{
		{Entry: &memory.MemoryEntry{ID: "low", Content: "低相关记忆"}, Score: 0.54},
	}}}
	agent := NewLibrarianAgent(
		librarianLLMFake{response: `{"character_names":[],"world_settings":[],"search_queries":["query"]}`},
		embedder,
		store,
		nil,
		nil,
		librarianTestConfig(),
	)

	result, err := agent.Run(context.Background(), &GenerationState{NovelID: "novel", ChapterIndex: 4})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Context, "前情提要与伏笔") {
		t.Fatalf("Context contains empty memory section: %q", result.Context)
	}
}

func TestLibrarianInjectsStaticAndDynamicLedgerState(t *testing.T) {
	characterRepo := &librarianCharacterRepositoryFake{characters: []*domain.Character{{
		NovelID:       "novel",
		Name:          "林云",
		Gender:        "男",
		Age:           20,
		Appearance:    "黑衣",
		Personality:   "谨慎",
		Background:    "边城出身",
		CurrentStatus: "已进入青云山密室",
	}}}
	worldRepo := &librarianWorldRepositoryFake{settings: []*domain.WorldSetting{{
		NovelID:     "novel",
		Category:    "地理",
		Name:        "青云山",
		Description: "终年云雾环绕的修炼宗门",
	}}}
	embedder := &librarianEmbedderFake{vectors: [][]float32{{1}}}
	store := &librarianVectorStoreFake{results: [][]memory.SearchResult{{}}}
	agent := NewLibrarianAgent(
		librarianLLMFake{response: `{"character_names":["林云"],"world_settings":["青云山"],"search_queries":["密室线索"]}`},
		embedder,
		store,
		characterRepo,
		worldRepo,
		librarianTestConfig(),
	)

	result, err := agent.Run(context.Background(), &GenerationState{NovelID: "novel", ChapterIndex: 4})
	if err != nil {
		t.Fatal(err)
	}
	if characterRepo.listCalls != 1 || characterRepo.findCalls != 0 ||
		worldRepo.listCalls != 1 || worldRepo.findCalls != 0 {
		t.Fatalf(
			"repository calls: character list=%d find=%d, world list=%d find=%d",
			characterRepo.listCalls,
			characterRepo.findCalls,
			worldRepo.listCalls,
			worldRepo.findCalls,
		)
	}
	if !reflect.DeepEqual(characterRepo.listChapterIndexes, []int{4}) || !reflect.DeepEqual(worldRepo.listChapterIndexes, []int{4}) {
		t.Fatalf("ledger chapter boundaries: characters=%#v world=%#v", characterRepo.listChapterIndexes, worldRepo.listChapterIndexes)
	}
	for _, value := range []string{
		"静态档案(性别:男, 年龄:20, 外貌:黑衣, 性格:谨慎, 背景:边城出身)",
		"当前状态:已进入青云山密室",
		"[地理] 青云山: 静态说明:终年云雾环绕的修炼宗门",
	} {
		if !strings.Contains(result.Context, value) {
			t.Fatalf("Context missing %q: %s", value, result.Context)
		}
	}
	if strings.Contains(result.Context, "青云山: 静态说明:终年云雾环绕的修炼宗门; 当前状态:") {
		t.Fatalf("Context contains empty world current-state label: %s", result.Context)
	}
	wantConstraints := []CanonConstraint{
		{
			Kind:      "character_static",
			Subject:   "林云",
			Statement: "角色林云的静态档案：性别:男；年龄:20；外貌:黑衣；性格:谨慎；背景:边城出身",
		},
		{
			Kind:      "character_current_status",
			Subject:   "林云",
			Statement: "角色林云当前状态：已进入青云山密室",
		},
		{
			Kind:      "world_static",
			Subject:   "青云山",
			Statement: "世界设定青云山的静态说明：终年云雾环绕的修炼宗门",
		},
	}
	if !reflect.DeepEqual(result.CanonConstraints, wantConstraints) {
		t.Fatalf("CanonConstraints = %#v, want %#v", result.CanonConstraints, wantConstraints)
	}
}

func TestCurrentChapterRetrievalFragmentsUsesOnlyPositiveCurrentChapterFields(t *testing.T) {
	state := &GenerationState{
		FullOutline:     "全书中的未来角色",
		ExistingOutline: "已有大纲中的未来地点",
		Outline:         "契约存在时不应扫描的大纲",
		ChapterContract: ChapterContract{
			Goal:          "目标林云",
			MustHappen:    []string{"必须抵达青云山"},
			MustNotHappen: []string{"禁止遇见苏青"},
			EndState:      "林云留在青云山",
		},
		MainlineBeat: MainlineEventBeat{
			CurrentEvent: "林云调查山门",
			NextEvent:    "苏青进入祭坛",
		},
		PreviousContinuity: ContinuityPacket{
			LastBeat:   "林云推开石门",
			OpenLoops:  []string{"青云山为何封闭"},
			NextAction: "林云检查门锁",
		},
		ManualContext: "青云山当前戒严",
		SceneCard:     "场景卡中的祭坛",
	}

	want := []string{
		"目标林云",
		"必须抵达青云山",
		"林云留在青云山",
		"林云调查山门",
		"林云推开石门",
		"青云山为何封闭",
		"林云检查门锁",
		"青云山当前戒严",
	}
	if got := currentChapterRetrievalFragments(state); !reflect.DeepEqual(got, want) {
		t.Fatalf("fragments = %#v, want %#v", got, want)
	}

	state.ChapterContract = ChapterContract{}
	if got := currentChapterRetrievalFragments(state); len(got) == 0 || got[0] != "契约存在时不应扫描的大纲" {
		t.Fatalf("empty-contract fragments = %#v, want Outline first", got)
	}
}

func TestAppendDeterministicEntitiesUsesConservativeCanonicalMatching(t *testing.T) {
	shortPlace := &domain.WorldSetting{Name: "青云"}
	longPlace := &domain.WorldSetting{Name: "青云山"}
	oneRune := &domain.Character{Name: "王"}
	li := &domain.Character{Name: "Li"}
	mixed := &domain.Character{Name: "第7小队"}
	asciiZone := &domain.WorldSetting{Name: "A区"}
	crossTypeCharacter := &domain.Character{Name: "天门"}
	crossTypeSetting := &domain.WorldSetting{Name: "天门"}
	catalogs := struct {
		characters characterCatalog
		world      worldCatalog
	}{
		characters: characterCatalog{
			byName: map[string]*domain.Character{"王": oneRune, "Li": li, "第7小队": mixed, "天门": crossTypeCharacter},
			allNames: map[string]struct{}{
				"王": {}, "Li": {}, "第7小队": {}, "天门": {},
			},
		},
		world: worldCatalog{
			byName: map[string]*domain.WorldSetting{"青云": shortPlace, "青云山": longPlace, "A区": asciiZone, "天门": crossTypeSetting},
			allNames: map[string]struct{}{
				"青云": {}, "青云山": {}, "A区": {}, "天门": {},
			},
		},
	}

	characters, settings := appendDeterministicEntities(
		nil,
		nil,
		[]string{"抵达青云山；王没有自动命中；天门存在歧义"},
		catalogs.characters,
		catalogs.world,
	)
	if len(characters) != 0 {
		t.Fatalf("characters = %#v, want none", characters)
	}
	if !reflect.DeepEqual(settings, []*domain.WorldSetting{longPlace}) {
		t.Fatalf("settings = %#v, want only longest place", settings)
	}

	characters, _ = appendDeterministicEntities(
		nil,
		nil,
		[]string{"AliceLi_2 does not contain a standalone name"},
		catalogs.characters,
		catalogs.world,
	)
	if len(characters) != 0 {
		t.Fatalf("ASCII substring matched without boundaries: %#v", characters)
	}
	characters, _ = appendDeterministicEntities(
		nil,
		nil,
		[]string{"(Li) is standalone"},
		catalogs.characters,
		catalogs.world,
	)
	if !reflect.DeepEqual(characters, []*domain.Character{li}) {
		t.Fatalf("standalone ASCII name did not match: %#v", characters)
	}

	characters, settings = appendDeterministicEntities(
		nil,
		nil,
		[]string{"第7小队出发并进入A区调查"},
		catalogs.characters,
		catalogs.world,
	)
	if !reflect.DeepEqual(characters, []*domain.Character{mixed}) || !reflect.DeepEqual(settings, []*domain.WorldSetting{asciiZone}) {
		t.Fatalf("mixed names did not match Chinese adjacency: characters=%#v settings=%#v", characters, settings)
	}
}

func TestDeterministicEntityCandidatesRejectsCrossTypeNameWhenOtherTypeIsInternallyAmbiguous(t *testing.T) {
	setting := &domain.WorldSetting{Name: "天门"}
	candidates := deterministicEntityCandidates(
		characterCatalog{
			byName:   map[string]*domain.Character{},
			allNames: map[string]struct{}{"天门": {}},
		},
		worldCatalog{
			byName:   map[string]*domain.WorldSetting{"天门": setting},
			allNames: map[string]struct{}{"天门": {}},
		},
	)
	if len(candidates) != 0 {
		t.Fatalf("candidates = %#v, want cross-type ambiguous name excluded", candidates)
	}
}

func TestLibrarianAddsEntitiesOmittedByRetrievalPlan(t *testing.T) {
	lin := &domain.Character{ID: "lin", NovelID: "novel", Name: "林云", CurrentStatus: "负伤"}
	mountain := &domain.WorldSetting{ID: "mountain", NovelID: "novel", Name: "青云山", CurrentState: "山门封闭"}
	characterRepo := &librarianCharacterRepositoryFake{characters: []*domain.Character{lin}}
	worldRepo := &librarianWorldRepositoryFake{settings: []*domain.WorldSetting{mountain}}
	agent := NewLibrarianAgent(
		librarianLLMFake{response: `{"character_names":[],"world_settings":[],"search_queries":[]}`},
		&librarianEmbedderFake{vectors: [][]float32{{1}}},
		&librarianVectorStoreFake{results: [][]memory.SearchResult{{}}},
		characterRepo,
		worldRepo,
		librarianTestConfig(),
	)
	state := &GenerationState{
		NovelID:      "novel",
		ChapterIndex: 4,
		ChapterContract: ChapterContract{
			Goal:       "林云调查异变",
			MustHappen: []string{"林云抵达青云山"},
			EndState:   "林云留在山门外",
		},
	}

	result, err := agent.Run(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"【相关角色卡】", "林云", "当前状态:负伤", "【世界观设定】", "青云山", "当前状态:山门封闭"} {
		if !strings.Contains(result.Context, value) {
			t.Fatalf("Context missing %q: %s", value, result.Context)
		}
	}
	if characterRepo.listCalls != 1 || characterRepo.findCalls != 0 || worldRepo.listCalls != 1 || worldRepo.findCalls != 0 {
		t.Fatalf("repository calls: character list=%d find=%d, world list=%d find=%d", characterRepo.listCalls, characterRepo.findCalls, worldRepo.listCalls, worldRepo.findCalls)
	}
	wantKinds := []string{"character_current_status", "world_current_state"}
	if len(result.CanonConstraints) != len(wantKinds) {
		t.Fatalf("CanonConstraints = %#v", result.CanonConstraints)
	}
	for index, kind := range wantKinds {
		if result.CanonConstraints[index].Kind != kind {
			t.Fatalf("CanonConstraints[%d] = %#v, want kind %q", index, result.CanonConstraints[index], kind)
		}
	}
}

func TestLibrarianDoesNotAddEntitiesFromFutureOrNegativeFields(t *testing.T) {
	future := &domain.Character{ID: "future", Name: "苏青"}
	place := &domain.WorldSetting{ID: "place", Name: "祭坛"}
	agent := NewLibrarianAgent(
		librarianLLMFake{response: `{"character_names":[],"world_settings":[],"search_queries":[]}`},
		&librarianEmbedderFake{vectors: [][]float32{{1}}},
		&librarianVectorStoreFake{results: [][]memory.SearchResult{{}}},
		&librarianCharacterRepositoryFake{characters: []*domain.Character{future}},
		&librarianWorldRepositoryFake{settings: []*domain.WorldSetting{place}},
		librarianTestConfig(),
	)
	state := &GenerationState{
		NovelID:         "novel",
		ChapterIndex:    4,
		FullOutline:     "苏青前往祭坛",
		ExistingOutline: "祭坛揭示真相",
		Outline:         "苏青发现祭坛",
		ChapterContract: ChapterContract{
			Goal:          "调查眼前线索",
			MustNotHappen: []string{"苏青进入祭坛"},
		},
		MainlineBeat: MainlineEventBeat{CurrentEvent: "检查石门", NextEvent: "苏青抵达祭坛"},
		SceneCard:    "苏青站在祭坛前",
	}

	result, err := agent.Run(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Context, "苏青") || strings.Contains(result.Context, "祭坛") || len(result.CanonConstraints) != 0 {
		t.Fatalf("future entities leaked into derived state: context=%q constraints=%#v", result.Context, result.CanonConstraints)
	}
}

func TestLibrarianMergesLLMAndDeterministicEntitiesInStableOrder(t *testing.T) {
	lin := &domain.Character{ID: "lin", Name: "林云", Personality: "谨慎"}
	su := &domain.Character{ID: "su", Name: "苏青", Personality: "果断"}
	mountain := &domain.WorldSetting{ID: "mountain", Name: "青云山", Description: "宗门所在"}
	city := &domain.WorldSetting{ID: "city", Name: "边城", Description: "北境城镇"}
	characterRepo := &librarianCharacterRepositoryFake{characters: []*domain.Character{su, lin}}
	worldRepo := &librarianWorldRepositoryFake{settings: []*domain.WorldSetting{city, mountain}}
	agent := NewLibrarianAgent(
		librarianLLMFake{response: `{"character_names":["苏青","苏青"],"world_settings":["边城"],"search_queries":[]}`},
		&librarianEmbedderFake{vectors: [][]float32{{1}}},
		&librarianVectorStoreFake{results: [][]memory.SearchResult{{}}},
		characterRepo,
		worldRepo,
		librarianTestConfig(),
	)
	state := &GenerationState{
		NovelID:      "novel",
		ChapterIndex: 4,
		ChapterContract: ChapterContract{
			Goal:       "林云前往青云山",
			MustHappen: []string{"苏青留在边城"},
		},
	}

	result, err := agent.Run(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	for _, order := range [][2]string{{"- 苏青:", "- 林云:"}, {"- [" + city.Category + "] 边城:", "- [" + mountain.Category + "] 青云山:"}} {
		left := strings.Index(result.Context, order[0])
		right := strings.Index(result.Context, order[1])
		if left < 0 || right < 0 || left >= right {
			t.Fatalf("Context order %q before %q not preserved: %s", order[0], order[1], result.Context)
		}
	}
	if strings.Count(result.Context, "- 苏青:") != 1 || strings.Count(result.Context, "] 边城:") != 1 {
		t.Fatalf("Context contains duplicate entities: %s", result.Context)
	}
	wantSubjects := []string{"苏青", "林云", "边城", "青云山"}
	if len(result.CanonConstraints) != len(wantSubjects) {
		t.Fatalf("CanonConstraints = %#v", result.CanonConstraints)
	}
	for index, subject := range wantSubjects {
		if result.CanonConstraints[index].Subject != subject {
			t.Fatalf("CanonConstraints[%d] = %#v, want subject %q", index, result.CanonConstraints[index], subject)
		}
	}
	if !reflect.DeepEqual(worldRepo.listNovelIDs, []string{"novel"}) {
		t.Fatalf("world repository novel scopes = %#v", worldRepo.listNovelIDs)
	}
}

func TestLibrarianOutputIsStableAcrossRepositoryOrder(t *testing.T) {
	characters := []*domain.Character{
		{ID: "lin", Name: "林云", Personality: "谨慎"},
		{ID: "su", Name: "苏青", Personality: "果断"},
	}
	settings := []*domain.WorldSetting{
		{ID: "mountain", Name: "青云山", Description: "宗门所在"},
		{ID: "city", Name: "边城", Description: "北境城镇"},
	}
	run := func(characters []*domain.Character, settings []*domain.WorldSetting) (string, []CanonConstraint) {
		agent := NewLibrarianAgent(
			librarianLLMFake{response: `{"character_names":[],"world_settings":[],"search_queries":[]}`},
			&librarianEmbedderFake{vectors: [][]float32{{1}}},
			&librarianVectorStoreFake{results: [][]memory.SearchResult{{}}},
			&librarianCharacterRepositoryFake{characters: characters},
			&librarianWorldRepositoryFake{settings: settings},
			librarianTestConfig(),
		)
		result, err := agent.Run(context.Background(), &GenerationState{
			NovelID:      "novel",
			ChapterIndex: 4,
			ChapterContract: ChapterContract{
				Goal: "林云、苏青从边城前往青云山",
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return result.Context, result.CanonConstraints
	}

	forwardContext, forwardConstraints := run(characters, settings)
	reverseContext, reverseConstraints := run([]*domain.Character{characters[1], characters[0]}, []*domain.WorldSetting{settings[1], settings[0]})
	if forwardContext != reverseContext {
		t.Fatalf("repository order changed Context:\nforward=%s\nreverse=%s", forwardContext, reverseContext)
	}
	if !reflect.DeepEqual(forwardConstraints, reverseConstraints) {
		t.Fatalf("repository order changed CanonConstraints:\nforward=%#v\nreverse=%#v", forwardConstraints, reverseConstraints)
	}
}

func TestLibrarianAllowsExactLLMSingleRuneAndRejectsAmbiguousName(t *testing.T) {
	king := &domain.Character{ID: "king", Name: "王", Personality: "沉稳"}
	first := &domain.Character{ID: "first", Name: "苏青", Personality: "果断"}
	second := &domain.Character{ID: "second", Name: "苏青", Personality: "谨慎"}
	agent := NewLibrarianAgent(
		librarianLLMFake{response: `{"character_names":["王","苏青"],"world_settings":[],"search_queries":[]}`},
		&librarianEmbedderFake{},
		&librarianVectorStoreFake{},
		&librarianCharacterRepositoryFake{characters: []*domain.Character{second, king, first}},
		nil,
		librarianTestConfig(),
	)

	result, err := agent.Run(context.Background(), &GenerationState{NovelID: "novel", ChapterIndex: 4})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Context, "- 王:") || strings.Contains(result.Context, "苏青") {
		t.Fatalf("LLM exact/ambiguous resolution incorrect: %s", result.Context)
	}
}

func TestLibrarianRejectsAmbiguousWorldSettingName(t *testing.T) {
	first := &domain.WorldSetting{ID: "first", Name: "青云山", Description: "宗门"}
	second := &domain.WorldSetting{ID: "second", Name: "青云山", Description: "山脉"}
	worldRepo := &librarianWorldRepositoryFake{settings: []*domain.WorldSetting{second, first}}
	agent := NewLibrarianAgent(
		librarianLLMFake{response: `{"character_names":[],"world_settings":["青云山"],"search_queries":[]}`},
		&librarianEmbedderFake{},
		&librarianVectorStoreFake{},
		nil,
		worldRepo,
		librarianTestConfig(),
	)

	result, err := agent.Run(context.Background(), &GenerationState{NovelID: "novel", ChapterIndex: 4})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Context, "青云山") || len(result.CanonConstraints) != 0 {
		t.Fatalf("ambiguous world setting was selected: context=%q constraints=%#v", result.Context, result.CanonConstraints)
	}
	if !reflect.DeepEqual(worldRepo.listNovelIDs, []string{"novel"}) {
		t.Fatalf("world repository novel scopes = %#v", worldRepo.listNovelIDs)
	}
}

func TestLibrarianPreservesDerivedStateWhenRelationshipProjectionFails(t *testing.T) {
	lin := &domain.Character{ID: "lin", Name: "林云", CurrentStatus: "负伤"}
	characterRepo := &librarianCharacterRepositoryFake{
		characters:      []*domain.Character{lin},
		relationshipErr: errors.New("relationships unavailable"),
	}
	agent := NewLibrarianAgent(
		librarianLLMFake{response: `{"character_names":["林云"],"world_settings":[],"search_queries":[]}`},
		&librarianEmbedderFake{},
		&librarianVectorStoreFake{},
		characterRepo,
		nil,
		librarianTestConfig(),
	)
	state := &GenerationState{
		NovelID:          "novel",
		ChapterIndex:     4,
		Context:          "旧背景",
		CanonConstraints: []CanonConstraint{{Kind: "old"}},
	}

	result, err := agent.Run(context.Background(), state)
	if err == nil || !strings.Contains(err.Error(), "librarian list relationships") {
		t.Fatalf("error = %v", err)
	}
	if result.Context != "旧背景" || len(result.CanonConstraints) != 1 || result.CanonConstraints[0].Kind != "old" {
		t.Fatalf("derived state changed: context=%q constraints=%#v", result.Context, result.CanonConstraints)
	}
	if characterRepo.relationshipCalls != 1 || !reflect.DeepEqual(characterRepo.relationshipChapterIndexes, []int{4}) {
		t.Fatalf("relationship calls=%d chapters=%#v", characterRepo.relationshipCalls, characterRepo.relationshipChapterIndexes)
	}
}

func TestLibrarianDeduplicatesDeterministicAndLLMQueries(t *testing.T) {
	embedder := &librarianEmbedderFake{vectors: [][]float32{{1}, {2}}}
	agent := NewLibrarianAgent(
		librarianLLMFake{response: `{"character_names":[],"world_settings":[],"search_queries":["调查血书","其他查询"]}`},
		embedder,
		&librarianVectorStoreFake{results: [][]memory.SearchResult{{}, {}}},
		nil,
		nil,
		librarianTestConfig(),
	)

	if _, err := agent.Run(context.Background(), &GenerationState{NovelID: "novel", ChapterIndex: 4, Outline: "调查血书"}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(embedder.inputs, [][]string{{"调查血书", "其他查询"}}) {
		t.Fatalf("EmbedBatch inputs = %#v", embedder.inputs)
	}
}

func TestLibrarianDoesNotCountSeedAgainstNeighborCardLimit(t *testing.T) {
	seed := &domain.Character{ID: "seed", Name: "林云"}
	characters := []*domain.Character{seed}
	relationships := make([]*domain.Relationship, 0, 8)
	for index := 1; index <= 8; index++ {
		neighbor := &domain.Character{
			ID:          fmt.Sprintf("neighbor-%d", index),
			Name:        fmt.Sprintf("邻居%d", index),
			Personality: fmt.Sprintf("性格%d", index),
		}
		characters = append(characters, neighbor)
		relationships = append(relationships, &domain.Relationship{
			SourceCharacter: &domain.Character{ID: "seed", Name: "林云"},
			TargetCharacter: &domain.Character{ID: neighbor.ID, Name: neighbor.Name},
			RelationType:    "盟友",
		})
	}
	agent := NewLibrarianAgent(
		librarianLLMFake{response: `{"character_names":["林云"],"world_settings":[],"search_queries":[]}`},
		&librarianEmbedderFake{},
		&librarianVectorStoreFake{},
		&librarianCharacterRepositoryFake{characters: characters, relationships: relationships},
		nil,
		librarianTestConfig(),
	)

	result, err := agent.Run(context.Background(), &GenerationState{NovelID: "novel", ChapterIndex: 4})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(result.Context, "- 林云:") != 1 || !strings.Contains(result.Context, "- 邻居8:") {
		t.Fatalf("seed consumed neighbor card limit: %s", result.Context)
	}
}

func TestLibrarianDeduplicatesRelationshipsBeforeLimit(t *testing.T) {
	seed := &domain.Character{ID: "seed", Name: "林云"}
	firstNeighbor := &domain.Character{ID: "first", Name: "苏青"}
	secondNeighbor := &domain.Character{ID: "second", Name: "赵峥"}
	duplicate := &domain.Relationship{
		SourceCharacter: &domain.Character{ID: "seed", Name: "林云"},
		TargetCharacter: &domain.Character{ID: "first", Name: "苏青"},
		RelationType:    "盟友",
		Description:     "共同追查血书",
	}
	relationships := make([]*domain.Relationship, 0, 11)
	for range 10 {
		copy := *duplicate
		relationships = append(relationships, &copy)
	}
	relationships = append(relationships, &domain.Relationship{
		SourceCharacter: &domain.Character{ID: "seed", Name: "林云"},
		TargetCharacter: &domain.Character{ID: "second", Name: "赵峥"},
		RelationType:    "师徒",
		Description:     "传授剑法",
	})
	characterRepo := &librarianCharacterRepositoryFake{
		characters:    []*domain.Character{seed, firstNeighbor, secondNeighbor},
		relationships: relationships,
	}
	agent := NewLibrarianAgent(
		librarianLLMFake{response: `{"character_names":["林云"],"world_settings":[],"search_queries":[]}`},
		&librarianEmbedderFake{},
		&librarianVectorStoreFake{},
		characterRepo,
		nil,
		librarianTestConfig(),
	)

	result, err := agent.Run(context.Background(), &GenerationState{NovelID: "novel", ChapterIndex: 4})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(result.Context, "共同追查血书") != 1 || !strings.Contains(result.Context, "传授剑法") {
		t.Fatalf("duplicate relationships displaced a distinct relation: %s", result.Context)
	}
}

func TestLibrarianFreezesDistinctRelationshipConstraintsOnce(t *testing.T) {
	lin := &domain.Character{ID: "lin", Name: "林云", CurrentStatus: "在密室搜索"}
	su := &domain.Character{ID: "su", Name: "苏青", Personality: "果断"}
	characterRepo := &librarianCharacterRepositoryFake{
		characters: []*domain.Character{lin, su},
		relationships: []*domain.Relationship{{
			SourceCharacter: &domain.Character{ID: "lin", Name: "林云"},
			TargetCharacter: &domain.Character{ID: "su", Name: "苏青"},
			RelationType:    "盟友",
			Description:     "共同追查血书",
		}, {
			SourceCharacter: &domain.Character{ID: "lin", Name: "林云"},
			TargetCharacter: &domain.Character{ID: "su", Name: "苏青"},
			RelationType:    "同门",
			Description:     "同出青云门",
		}},
	}
	agent := NewLibrarianAgent(
		librarianLLMFake{response: `{"character_names":["林云"],"world_settings":[],"search_queries":["盟友关系"]}`},
		&librarianEmbedderFake{vectors: [][]float32{{1}}},
		&librarianVectorStoreFake{results: [][]memory.SearchResult{{}}},
		characterRepo,
		nil,
		librarianTestConfig(),
	)

	result, err := agent.Run(context.Background(), &GenerationState{NovelID: "novel", ChapterIndex: 4})
	if err != nil {
		t.Fatal(err)
	}
	if characterRepo.listCalls != 1 || characterRepo.findCalls != 0 || characterRepo.relationshipCalls != 1 {
		t.Fatalf("repository calls: list=%d find=%d relationships=%d", characterRepo.listCalls, characterRepo.findCalls, characterRepo.relationshipCalls)
	}
	if !reflect.DeepEqual(characterRepo.listNovelIDs, []string{"novel"}) ||
		!reflect.DeepEqual(characterRepo.relationshipNovelIDs, []string{"novel"}) {
		t.Fatalf("repository novel scopes: list=%#v relationships=%#v", characterRepo.listNovelIDs, characterRepo.relationshipNovelIDs)
	}
	wantKinds := []string{
		"character_current_status",
		"character_relationship",
		"character_relationship",
		"character_static",
	}
	if len(result.CanonConstraints) != len(wantKinds) {
		t.Fatalf("CanonConstraints = %#v", result.CanonConstraints)
	}
	for index, kind := range wantKinds {
		if result.CanonConstraints[index].Kind != kind {
			t.Fatalf("CanonConstraints[%d] = %#v, want kind %q", index, result.CanonConstraints[index], kind)
		}
	}
	if strings.Count(result.CanonConstraints[0].Statement, "林云") != 1 ||
		result.CanonConstraints[1].Subject != "林云->苏青[同门]" ||
		result.CanonConstraints[1].Statement != "角色关系：林云与苏青是同门；同出青云门" ||
		result.CanonConstraints[2].Subject != "林云->苏青[盟友]" ||
		result.CanonConstraints[2].Statement != "角色关系：林云与苏青是盟友；共同追查血书" ||
		result.CanonConstraints[3].Statement != "角色苏青的静态档案：性格:果断" {
		t.Fatalf("CanonConstraints = %#v", result.CanonConstraints)
	}
}

func TestLibrarianIgnoresRelationshipEndpointsOutsideCanonicalCatalog(t *testing.T) {
	canonical := &domain.Character{ID: "canonical", Name: "林云", CurrentStatus: "在密室搜索"}
	impostor := &domain.Character{ID: "other", Name: "林云"}
	neighbor := &domain.Character{ID: "neighbor", Name: "苏青"}
	characterRepo := &librarianCharacterRepositoryFake{
		characters: []*domain.Character{canonical, neighbor},
		relationships: []*domain.Relationship{{
			SourceCharacter: impostor,
			TargetCharacter: neighbor,
			RelationType:    "盟友",
		}},
	}
	agent := NewLibrarianAgent(
		librarianLLMFake{response: `{"character_names":["林云"],"world_settings":[],"search_queries":["关系"]}`},
		&librarianEmbedderFake{vectors: [][]float32{{1}}},
		&librarianVectorStoreFake{results: [][]memory.SearchResult{{}}},
		characterRepo,
		nil,
		librarianTestConfig(),
	)

	result, err := agent.Run(context.Background(), &GenerationState{NovelID: "novel", ChapterIndex: 4})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Context, "角色关系网") || strings.Contains(result.Context, "苏青") {
		t.Fatalf("Context contains non-canonical relationship endpoint: %s", result.Context)
	}
}

func TestLibrarianIgnoresAmbiguousCanonicalRelationshipNeighbor(t *testing.T) {
	seed := &domain.Character{ID: "seed", Name: "林云"}
	firstNeighbor := &domain.Character{ID: "first", Name: "苏青", Personality: "果断"}
	secondNeighbor := &domain.Character{ID: "second", Name: "苏青", Personality: "谨慎"}
	characterRepo := &librarianCharacterRepositoryFake{
		characters: []*domain.Character{seed, firstNeighbor, secondNeighbor},
		relationships: []*domain.Relationship{{
			SourceCharacter: seed,
			TargetCharacter: firstNeighbor,
			RelationType:    "盟友",
		}},
	}
	agent := NewLibrarianAgent(
		librarianLLMFake{response: `{"character_names":["林云"],"world_settings":[],"search_queries":["关系"]}`},
		&librarianEmbedderFake{vectors: [][]float32{{1}}},
		&librarianVectorStoreFake{results: [][]memory.SearchResult{{}}},
		characterRepo,
		nil,
		librarianTestConfig(),
	)

	result, err := agent.Run(context.Background(), &GenerationState{NovelID: "novel", ChapterIndex: 4})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Context, "角色关系网") || strings.Contains(result.Context, "苏青") {
		t.Fatalf("Context contains ambiguous canonical neighbor: %s", result.Context)
	}
	for _, constraint := range result.CanonConstraints {
		if strings.Contains(constraint.Subject, "苏青") || strings.Contains(constraint.Statement, "苏青") {
			t.Fatalf("CanonConstraints contain ambiguous canonical neighbor: %#v", result.CanonConstraints)
		}
	}
}

func TestLibrarianIncludesWorldCurrentState(t *testing.T) {
	worldRepo := &worldRepositoryFake{existing: &domain.WorldSetting{
		NovelID:      "novel",
		Category:     "地理",
		Name:         "青云山",
		Description:  "终年云雾环绕的修炼宗门",
		CurrentState: "山门封闭并由长老守卫",
	}}
	embedder := &librarianEmbedderFake{vectors: [][]float32{{1}}}
	store := &librarianVectorStoreFake{results: [][]memory.SearchResult{{}}}
	agent := NewLibrarianAgent(
		librarianLLMFake{response: `{"character_names":[],"world_settings":["青云山"],"search_queries":["山门状态"]}`},
		embedder,
		store,
		nil,
		worldRepo,
		librarianTestConfig(),
	)

	result, err := agent.Run(context.Background(), &GenerationState{NovelID: "novel", ChapterIndex: 4})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Context, "静态说明:终年云雾环绕的修炼宗门; 当前状态:山门封闭并由长老守卫") {
		t.Fatalf("Context = %s", result.Context)
	}
	want := []CanonConstraint{
		{Kind: "world_static", Subject: "青云山", Statement: "世界设定青云山的静态说明：终年云雾环绕的修炼宗门"},
		{Kind: "world_current_state", Subject: "青云山", Statement: "世界设定青云山当前状态：山门封闭并由长老守卫"},
	}
	if !reflect.DeepEqual(result.CanonConstraints, want) {
		t.Fatalf("CanonConstraints = %#v, want %#v", result.CanonConstraints, want)
	}
}

func TestLibrarianPreservesDerivedStateWhenCatalogEnumerationFails(t *testing.T) {
	tests := []struct {
		name      string
		charRepo  domain.CharacterRepository
		worldRepo domain.WorldRepository
		wantError string
	}{
		{
			name:      "character catalog",
			charRepo:  &librarianCharacterRepositoryFake{listErr: errors.New("characters unavailable")},
			wantError: "librarian list characters",
		},
		{
			name:      "world catalog",
			worldRepo: &librarianWorldRepositoryFake{listErr: errors.New("world unavailable")},
			wantError: "librarian list world settings",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			agent := NewLibrarianAgent(
				librarianLLMFake{response: `{"character_names":[],"world_settings":[],"search_queries":["query"]}`},
				&librarianEmbedderFake{vectors: [][]float32{{1}}},
				&librarianVectorStoreFake{results: [][]memory.SearchResult{{}}},
				test.charRepo,
				test.worldRepo,
				librarianTestConfig(),
			)
			state := &GenerationState{
				NovelID:          "novel",
				ChapterIndex:     4,
				Context:          "旧背景",
				CanonConstraints: []CanonConstraint{{Kind: "old"}},
			}

			result, err := agent.Run(context.Background(), state)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want %q", err, test.wantError)
			}
			if result.Context != "旧背景" || len(result.CanonConstraints) != 1 || result.CanonConstraints[0].Kind != "old" {
				t.Fatalf("derived state changed on failure: context = %q, constraints = %#v", result.Context, result.CanonConstraints)
			}
		})
	}
}

func TestLibrarianPropagatesRetrievalFailures(t *testing.T) {
	retrievalErr := errors.New("retrieval unavailable")
	tests := []struct {
		name      string
		embedder  *librarianEmbedderFake
		store     *librarianVectorStoreFake
		wantError string
	}{
		{
			name:      "embedding failure",
			embedder:  &librarianEmbedderFake{err: retrievalErr},
			store:     &librarianVectorStoreFake{},
			wantError: "librarian embed queries",
		},
		{
			name:      "embedding count mismatch",
			embedder:  &librarianEmbedderFake{vectors: nil},
			store:     &librarianVectorStoreFake{},
			wantError: "got 0 vectors for 1 queries",
		},
		{
			name:      "search failure",
			embedder:  &librarianEmbedderFake{vectors: [][]float32{{1}}},
			store:     &librarianVectorStoreFake{err: retrievalErr},
			wantError: "librarian search query 0",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			agent := NewLibrarianAgent(
				librarianLLMFake{response: `{"character_names":[],"world_settings":[],"search_queries":["query"]}`},
				test.embedder,
				test.store,
				nil,
				nil,
				librarianTestConfig(),
			)
			state := &GenerationState{
				NovelID:          "novel",
				ChapterIndex:     4,
				Context:          "旧背景",
				CanonConstraints: []CanonConstraint{{Kind: "old"}},
			}
			result, err := agent.Run(context.Background(), state)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want %q", err, test.wantError)
			}
			if result.Context != "旧背景" || len(result.CanonConstraints) != 1 || result.CanonConstraints[0].Kind != "old" {
				t.Fatalf("derived state changed on failure: context = %q, constraints = %#v", result.Context, result.CanonConstraints)
			}
		})
	}
}
