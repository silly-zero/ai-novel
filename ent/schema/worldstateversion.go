package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type WorldStateVersion struct {
	ent.Schema
}

func (WorldStateVersion) Fields() []ent.Field {
	return []ent.Field{
		field.Int("world_setting_id").Positive(),
		field.Int("chapter_id").Positive(),
		field.Int("chapter_index").Positive(),
		field.String("generation_id"),
		field.Text("current_state"),
		field.Time("created_at").Default(time.Now),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (WorldStateVersion) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("chapter", Chapter.Type).
			Ref("world_state_versions").
			Field("chapter_id").
			Unique().
			Required(),
		edge.From("world_setting", WorldSetting.Type).
			Ref("state_versions").
			Field("world_setting_id").
			Unique().
			Required(),
	}
}

func (WorldStateVersion) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("world_setting_id", "chapter_id").Unique(),
		index.Fields("world_setting_id", "chapter_index"),
		index.Fields("chapter_id"),
	}
}
