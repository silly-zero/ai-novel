package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// Chapter holds the schema definition for the Chapter entity.
type Chapter struct {
	ent.Schema
}

// Fields of the Chapter.
func (Chapter) Fields() []ent.Field {
	return []ent.Field{
		field.String("title"),
		field.Text("content"),
		field.Int("word_count"),
		field.Int("order"),
		field.String("status").Default("Draft"),
		field.String("derived_status").Default("Ready"),
		field.String("derived_generation_id").Default(""),
		field.Text("last_beat").Default(""),
		field.JSON("open_loops", []string{}).Optional(),
		field.Text("next_action").Default(""),
		field.Time("created_at").Default(time.Now),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

// Edges of the Chapter.
func (Chapter) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("novel", Novel.Type).
			Ref("chapters").
			Unique().
			Required(),
		edge.To("character_state_versions", CharacterStateVersion.Type).
			Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("world_state_versions", WorldStateVersion.Type).
			Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("relationship_state_versions", RelationshipStateVersion.Type).
			Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("derived_tasks", ChapterDerivedTask.Type).
			Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}
