package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// Novel holds the schema definition for the Novel entity.
type Novel struct {
	ent.Schema
}

// Fields of the Novel.
func (Novel) Fields() []ent.Field {
	return []ent.Field{
		field.String("title"),
		field.Text("description").Optional(),
		field.Text("idea").Optional(),
		field.Text("outline").Optional(),
		field.String("status").Default("Draft"),
		field.JSON("tags", []string{}).Optional(),
		field.Time("created_at").Default(time.Now),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

// Edges of the Novel.
func (Novel) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("chapters", Chapter.Type),
	}
}
