package agents

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ai-novel/studio/internal/domain/memory"
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
			_, err := agent.Run(context.Background(), &GenerationState{NovelID: "novel"})
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want %q", err, test.wantError)
			}
		})
	}
}
