package novel

import (
	"fmt"
	"strings"
)

type RelationshipOperation string

const (
	RelationshipOperationUpsert RelationshipOperation = "upsert"
	RelationshipOperationRemove RelationshipOperation = "remove"
)

type RelationshipChange struct {
	SourceCharacter *Character
	TargetCharacter *Character
	RelationType    string
	Description     string
	Operation       RelationshipOperation
}

func (c *RelationshipChange) Normalize() {
	c.RelationType = strings.TrimSpace(c.RelationType)
	c.Description = strings.TrimSpace(c.Description)
	if c.Operation == "" {
		c.Operation = RelationshipOperationUpsert
	}
}

func (c RelationshipChange) Validate() error {
	if c.SourceCharacter == nil || strings.TrimSpace(c.SourceCharacter.ID) == "" {
		return fmt.Errorf("relationship source character is required")
	}
	if c.TargetCharacter == nil || strings.TrimSpace(c.TargetCharacter.ID) == "" {
		return fmt.Errorf("relationship target character is required")
	}
	if c.RelationType == "" {
		return fmt.Errorf("relationship type is required")
	}
	if c.Operation != RelationshipOperationUpsert && c.Operation != RelationshipOperationRemove {
		return fmt.Errorf("invalid relationship operation %q", c.Operation)
	}
	return nil
}
