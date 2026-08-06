package agents

import (
	"context"
	"errors"
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
	listErr            error
	saveCharacterErr   error
	saveRelationErr    error
	saveCharacterCalls int
	lastCharacterName  string
}

func (r *characterRepositoryFake) SaveCharacter(_ context.Context, c *domain.Character) error {
	r.saveCharacterCalls++
	r.lastCharacterName = c.Name
	if r.saveCharacterErr == nil {
		c.ID = "1"
	}
	return r.saveCharacterErr
}

func (*characterRepositoryFake) GetCharacter(context.Context, string) (*domain.Character, error) {
	return nil, errors.New("not found")
}

func (*characterRepositoryFake) FindByName(context.Context, string, string) (*domain.Character, error) {
	return nil, errors.New("not found")
}

func (r *characterRepositoryFake) ListCharacters(context.Context, string) ([]*domain.Character, error) {
	return nil, r.listErr
}

func (r *characterRepositoryFake) SaveRelationship(context.Context, *domain.Relationship) error {
	return r.saveRelationErr
}

func (*characterRepositoryFake) ListRelationships(context.Context, string) ([]*domain.Relationship, error) {
	return nil, nil
}

type worldRepositoryFake struct {
	listErr   error
	saveErr   error
	saveCalls int
}

func (r *worldRepositoryFake) SaveSetting(context.Context, *domain.WorldSetting) error {
	r.saveCalls++
	return r.saveErr
}

func (*worldRepositoryFake) FindByName(context.Context, string, string) (*domain.WorldSetting, error) {
	return nil, errors.New("not found")
}

func (*worldRepositoryFake) ListByCategory(context.Context, string, string) ([]*domain.WorldSetting, error) {
	return nil, nil
}

func (r *worldRepositoryFake) ListAll(context.Context, string) ([]*domain.WorldSetting, error) {
	return nil, r.listErr
}

func TestCharacterAgentReturnsRepositoryErrors(t *testing.T) {
	listErr := errors.New("list failed")
	agent := NewCharacterAgent(memoryAgentTestLLM{}, &characterRepositoryFake{listErr: listErr})
	if _, err := agent.Run(context.Background(), &GenerationState{NovelID: "7"}); !errors.Is(err, listErr) {
		t.Fatalf("list error = %v, want %v", err, listErr)
	}

	saveErr := errors.New("save character failed")
	agent = NewCharacterAgent(memoryAgentTestLLM{response: `{"characters":[{"name":"林云"}]}`}, &characterRepositoryFake{saveCharacterErr: saveErr})
	if _, err := agent.Run(context.Background(), &GenerationState{NovelID: "7"}); !errors.Is(err, saveErr) {
		t.Fatalf("save character error = %v, want %v", err, saveErr)
	}

	relationErr := errors.New("save relationship failed")
	agent = NewCharacterAgent(memoryAgentTestLLM{response: `{"characters":[{"name":"林云"},{"name":"苏青"}],"relationships":[{"source":"林云","target":"苏青","relation_type":"盟友"}]}`}, &characterRepositoryFake{saveRelationErr: relationErr})
	if _, err := agent.Run(context.Background(), &GenerationState{NovelID: "7"}); !errors.Is(err, relationErr) {
		t.Fatalf("save relationship error = %v, want %v", err, relationErr)
	}
}

func TestWorldAgentReturnsRepositoryErrors(t *testing.T) {
	listErr := errors.New("list failed")
	agent := NewWorldAgent(memoryAgentTestLLM{}, &worldRepositoryFake{listErr: listErr})
	if _, err := agent.Run(context.Background(), &GenerationState{NovelID: "7"}); !errors.Is(err, listErr) {
		t.Fatalf("list error = %v, want %v", err, listErr)
	}

	saveErr := errors.New("save setting failed")
	agent = NewWorldAgent(memoryAgentTestLLM{response: `[{"category":"地理","name":"青云山"}]`}, &worldRepositoryFake{saveErr: saveErr})
	if _, err := agent.Run(context.Background(), &GenerationState{NovelID: "7"}); !errors.Is(err, saveErr) {
		t.Fatalf("save setting error = %v, want %v", err, saveErr)
	}
}
