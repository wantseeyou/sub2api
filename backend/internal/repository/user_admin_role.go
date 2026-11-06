package repository

import (
	"context"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/adminrole"
	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"
	apperrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// resolveUserAdminRole 在用户写事务内验证角色，并锁定角色以保护并发删除和停用。
func resolveUserAdminRole(ctx context.Context, client *dbent.Client, target *service.User) error {
	target.AdminRole = nil
	if target.AdminRoleID == nil {
		return nil
	}
	if *target.AdminRoleID <= 0 || target.Role != service.RoleUser {
		return apperrors.BadRequest("INVALID_ADMIN_ROLE", "自定义角色只能分配给普通用户身份")
	}
	role, err := client.AdminRole.Query().Where(adminrole.IDEQ(*target.AdminRoleID), adminrole.Enabled(true)).ForShare().Only(ctx)
	if dbent.IsNotFound(err) {
		return apperrors.BadRequest("INVALID_ADMIN_ROLE", "角色不存在或已停用")
	}
	if err != nil {
		return err
	}
	target.AdminRole = service.AdminRoleFromEntity(role)
	return nil
}

// guardRoleUserMutation 在写事务内锁定目标，避免后台成员被低权限角色改密提权。
func guardRoleUserMutation(ctx context.Context, client *dbent.Client, id int64) error {
	return service.LockRoleUserMutationTarget(ctx, client, id)
}

// roleUserMutationSQLClause 在原子写入时限制目标身份，prefix 仅由仓库内部指定表别名。
func roleUserMutationSQLClause(ctx context.Context, prefix string) string {
	if adminaccess.RoleFromContext(ctx) == nil {
		return ""
	}
	return " AND " + prefix + "role = 'user' AND " + prefix + "admin_role_id IS NULL"
}
