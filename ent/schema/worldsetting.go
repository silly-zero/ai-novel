package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

// WorldSetting holds the schema definition for the WorldSetting entity.
type WorldSetting struct {
	ent.Schema
}

// Fields of the WorldSetting.
func (WorldSetting) Fields() []ent.Field {
	return []ent.Field{
		field.String("novel_id"),
		field.String("category"), // 如：地理、武学等级、势力、宝物、传说
		field.String("name"),     // 设定名称，如“青阳镇”、“大荒囚天指”
		field.Text("description"),
		field.JSON("metadata", map[string]interface{}{}).Optional(),
		field.Time("created_at").Default(time.Now),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

// Edges of the WorldSetting.
func (WorldSetting) Edges() []ent.Edge {
	return nil
}
