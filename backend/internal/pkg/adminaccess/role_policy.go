package adminaccess

import (
	"context"
	"fmt"
	"slices"
)

// RoleAccess 是当前网页登录请求使用的角色快照。
type RoleAccess struct {
	ID          int64    // 角色标识。
	Name        string   // 角色名称。
	Permissions []string // 当前请求从数据库读取的权限。
}

type roleContextKey struct{}

// WithRole 将自定义角色绑定到当前请求，不改变管理员 API Key 的授权上下文。
func WithRole(ctx context.Context, role *RoleAccess) context.Context {
	return context.WithValue(ctx, roleContextKey{}, role)
}

// RoleFromContext 读取当前网页登录请求的自定义角色。
func RoleFromContext(ctx context.Context) *RoleAccess {
	role, _ := ctx.Value(roleContextKey{}).(*RoleAccess)
	return role
}

// Allows 检查一个显式业务操作权限。
func (r *RoleAccess) Allows(permission string) bool {
	return r != nil && slices.Contains(r.Permissions, permission)
}

// RolePermissionCatalog 返回角色配置界面使用的完整权限目录。
func RolePermissionCatalog() []RolePermission { return slices.Clone(roleDefinitions) }

// RoleRoutePermissions 返回接口精确登记的可选权限，未登记的接口默认拒绝。
func RoleRoutePermissions(method, path string) []string {
	return slices.Clone(roleRoutes[method+" "+path])
}

// roleRoutes 在启动时编译精确接口映射，避免每次请求扫描整个目录。
var roleRoutes = func() map[string][]string {
	routes := make(map[string][]string)
	for _, definition := range roleDefinitions {
		for _, route := range definition.Routes {
			routes[route] = append(routes[route], definition.ID)
		}
	}
	return routes
}()

// ValidateRolePermissions 拒绝未知权限，并归一化可持久化的权限集合。
func ValidateRolePermissions(permissions []string) ([]string, error) {
	known := make(map[string]bool, len(roleDefinitions))
	for _, definition := range roleDefinitions {
		known[definition.ID] = true
	}
	if len(permissions) > len(known) {
		return nil, fmt.Errorf("权限数量超过限制")
	}
	result := slices.Clone(permissions)
	if result == nil {
		result = []string{}
	}
	for _, permission := range result {
		if !known[permission] {
			return nil, fmt.Errorf("无效的权限：%s", permission)
		}
	}
	slices.Sort(result)
	return slices.Compact(result), nil
}
