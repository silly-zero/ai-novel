package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// Relationship holds the schema definition for the Relationship entity.
type Relationship struct {
	ent.Schema
}

// Fields of the Relationship.
func (Relationship) Fields() []ent.Field {
	return []ent.Field{
		field.String("novel_id"),
		field.String("relation_type"), // 如：师徒、敌人、恋人
		field.Text("description").Optional(),
		field.Time("created_at").Default(time.Now),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

// Edges of the Relationship.
func (Relationship) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("character", Character.Type).
			Ref("relationships").
			Unique().
			Required(),
		edge.To("target_character", Character.Type).
			Unique().
			Required(),
	}
}
