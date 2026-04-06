package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

// MemoryEntry holds the schema definition for the MemoryEntry entity.
type MemoryEntry struct {
	ent.Schema
}

// Fields of the MemoryEntry.
func (MemoryEntry) Fields() []ent.Field {
	return []ent.Field{
		field.String("novel_id"),
		field.Text("content"),
		field.JSON("metadata", map[string]interface{}{}),
		field.JSON("embedding", []float32{}),
		field.Time("created_at").Default(time.Now),
	}
}

// Edges of the MemoryEntry.
func (MemoryEntry) Edges() []ent.Edge {
	return nil
}
