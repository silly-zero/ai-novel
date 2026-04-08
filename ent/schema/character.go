package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// Character holds the schema definition for the Character entity.
type Character struct {
	ent.Schema
}

// Fields of the Character.
func (Character) Fields() []ent.Field {
	return []ent.Field{
		field.String("novel_id"),
		field.String("name"),
		field.String("gender").Optional(),
		field.Int("age").Optional(),
		field.Text("appearance").Optional(),
		field.Text("personality").Optional(),
		field.Text("background").Optional(),
		field.Text("current_status").Optional(),
		field.Time("created_at").Default(time.Now),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

// Edges of the Character.
func (Character) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("relationships", Relationship.Type),
	}
}
