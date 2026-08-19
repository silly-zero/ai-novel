package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type CharacterStateVersion struct {
	ent.Schema
}

func (CharacterStateVersion) Fields() []ent.Field {
	return []ent.Field{
		field.Int("character_id").Positive(),
		field.Int("chapter_id").Positive(),
		field.Int("chapter_index").Positive(),
		field.String("generation_id"),
		field.Text("current_status"),
		field.Bool("valid").Default(true),
		field.Time("created_at").Default(time.Now),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (CharacterStateVersion) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("chapter", Chapter.Type).
			Ref("character_state_versions").
			Field("chapter_id").
			Unique().
			Required(),
		edge.From("character", Character.Type).
			Ref("state_versions").
			Field("character_id").
			Unique().
			Required(),
	}
}

func (CharacterStateVersion) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("character_id", "chapter_id").Unique(),
		index.Fields("character_id", "chapter_index"),
		index.Fields("chapter_id"),
	}
}
