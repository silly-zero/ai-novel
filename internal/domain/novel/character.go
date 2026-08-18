package novel

import (
	"context"
	"time"
)

// Character 角色领域模型
type Character struct {
	ID            string
	NovelID       string
	Name          string
	Gender        string
	Age           int
	Appearance    string
	Personality   string
	Background    string
	CurrentStatus string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Relationship 角色关系领域模型
type Relationship struct {
	ID              string
	NovelID         string
	SourceCharacter *Character
	TargetCharacter *Character
	RelationType    string
	Description     string
}

// CharacterRepository 角色持久化接口
type CharacterRepository interface {
	GetCharacter(ctx context.Context, id string) (*Character, error)
	FindByName(ctx context.Context, novelID, name string) (*Character, error)
	ListCharacters(ctx context.Context, novelID string) ([]*Character, error)
	ListCharactersBeforeChapter(ctx context.Context, novelID string, chapterIndex int) ([]*Character, error)
	ReplaceChapterCharacters(ctx context.Context, ref ChapterStateRef, characters []*Character) ([]*Character, error)
	ReplaceChapterRelationships(ctx context.Context, ref ChapterStateRef, changes []RelationshipChange) ([]*Relationship, error)
	ListRelationships(ctx context.Context, novelID string) ([]*Relationship, error)
	ListRelationshipsBeforeChapter(ctx context.Context, novelID string, chapterIndex int) ([]*Relationship, error)
}
