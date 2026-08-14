package agents

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ai-novel/studio/internal/domain/memory"
	domain "github.com/ai-novel/studio/internal/domain/novel"
)

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
	state := &GenerationState{NovelID: "novel-1", Outline: "outline"}

	result, err := agent.Run(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(embedder.inputs, [][]string{{"查询一", "查询二"}}) {
		t.Fatalf("EmbedBatch inputs = %#v", embedder.inputs)
	}
	if len(store.calls) != 2 {
		t.Fatalf("search calls = %d, want 2", len(store.calls))
	}
	for _, call := range store.calls {
		if call.novelID != "novel-1" || call.options != librarianTestConfig().SearchOptions {
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

	result, err := agent.Run(context.Background(), &GenerationState{NovelID: "novel"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Context, "前情提要与伏笔") {
		t.Fatalf("Context contains empty memory section: %q", result.Context)
	}
}

func TestLibrarianInjectsStaticAndDynamicLedgerState(t *testing.T) {
	characterRepo := &characterRepositoryFake{existing: &domain.Character{
		NovelID:       "novel",
		Name:          "林云",
		Gender:        "男",
		Age:           20,
		Appearance:    "黑衣",
		Personality:   "谨慎",
		Background:    "边城出身",
		CurrentStatus: "已进入青云山密室",
	}}
	worldRepo := &worldRepositoryFake{existing: &domain.WorldSetting{
		NovelID:     "novel",
		Category:    "地理",
		Name:        "青云山",
		Description: "终年云雾环绕的修炼宗门",
	}}
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

	result, err := agent.Run(context.Background(), &GenerationState{NovelID: "novel"})
	if err != nil {
		t.Fatal(err)
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

func TestLibrarianFreezesDistinctRelationshipConstraintsOnce(t *testing.T) {
	lin := &domain.Character{Name: "林云", CurrentStatus: "在密室搜索"}
	su := &domain.Character{Name: "苏青", Personality: "果断"}
	characterRepo := &characterRepositoryFake{
		existing: lin,
		byName: map[string]*domain.Character{
			"林云": lin,
			"苏青": su,
		},
		relationships: []*domain.Relationship{{
			SourceCharacter: lin,
			TargetCharacter: su,
			RelationType:    "盟友",
			Description:     "共同追查血书",
		}, {
			SourceCharacter: lin,
			TargetCharacter: su,
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

	result, err := agent.Run(context.Background(), &GenerationState{NovelID: "novel"})
	if err != nil {
		t.Fatal(err)
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

	result, err := agent.Run(context.Background(), &GenerationState{NovelID: "novel"})
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
