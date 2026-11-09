package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
)

// AdminRole 保存可分配给站内用户的后台角色。
type AdminRole struct {
	ent.Schema // 复用 Ent 的结构定义。
}

// Annotations 指定后台角色的数据表。
func (AdminRole) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "admin_roles"}}
}

// Mixin 统一记录角色的创建和更新时间。
func (AdminRole) Mixin() []ent.Mixin { return []ent.Mixin{mixins.TimeMixin{}} }

// Fields 定义角色名称、状态和显式权限集合。
func (AdminRole) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").MaxLen(64).NotEmpty().Unique().Comment("角色名称"),
		field.String("description").Default("").Comment("角色说明"),
		field.Bool("enabled").Default(true).Comment("是否启用后台访问"),
		field.JSON("permissions", []string{}).Default([]string{}).Comment("显式授予的后台权限"),
	}
}

// Edges 关联使用该角色的站内用户。
func (AdminRole) Edges() []ent.Edge { return []ent.Edge{edge.To("users", User.Type)} }
