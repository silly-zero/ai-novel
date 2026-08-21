package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type ChapterDerivedTask struct {
	ent.Schema
}

func (ChapterDerivedTask) Fields() []ent.Field {
	return []ent.Field{
		field.Int("chapter_id").Positive(),
		field.String("generation_id"),
		field.Enum("handler_key").Values("memory", "character", "world"),
		field.Enum("status").Values("Pending", "Running", "Ready", "Failed"),
		field.Int("attempts").Default(0).NonNegative(),
		field.String("lease_token").Default(""),
		field.Time("lease_until").Optional().Nillable(),
		field.Text("last_error").Default(""),
		field.Time("created_at").Default(time.Now),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (ChapterDerivedTask) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("chapter", Chapter.Type).
			Ref("derived_tasks").
			Field("chapter_id").
			Unique().
			Required(),
	}
}

func (ChapterDerivedTask) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("chapter_id", "generation_id", "handler_key").Unique(),
		index.Fields("chapter_id", "generation_id", "status"),
		index.Fields("lease_until"),
	}
}
