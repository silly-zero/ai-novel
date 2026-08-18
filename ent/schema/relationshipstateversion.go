package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type RelationshipStateVersion struct {
	ent.Schema
}

func (RelationshipStateVersion) Fields() []ent.Field {
	return []ent.Field{
		field.Int("chapter_id").Positive(),
		field.Int("source_character_id").Positive(),
		field.Int("target_character_id").Positive(),
		field.Int("chapter_index").Positive(),
		field.String("generation_id"),
		field.String("relation_type"),
		field.Text("description").Optional(),
		field.Bool("active"),
		field.Enum("operation").Values("upsert", "remove"),
		field.Time("created_at").Default(time.Now),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (RelationshipStateVersion) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("chapter", Chapter.Type).
			Ref("relationship_state_versions").
			Field("chapter_id").
			Unique().
			Required(),
		edge.From("source_character", Character.Type).
			Ref("source_relationship_state_versions").
			Field("source_character_id").
			Unique().
			Required(),
		edge.From("target_character", Character.Type).
			Ref("target_relationship_state_versions").
			Field("target_character_id").
			Unique().
			Required(),
	}
}

func (RelationshipStateVersion) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("source_character_id", "target_character_id", "relation_type", "chapter_id").Unique(),
		index.Fields("source_character_id", "target_character_id", "relation_type", "chapter_index"),
		index.Fields("chapter_id"),
	}
}
