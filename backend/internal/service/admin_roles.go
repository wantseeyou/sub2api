package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/adminrole"
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
	"github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"
	apperrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// AdminRole 表示可独立配置权限的后台角色。
type AdminRole struct {
	ID          int64     `json:"id"`          // 角色标识。
	Name        string    `json:"name"`        // 角色名称。
	Description string    `json:"description"` // 角色说明。
	Enabled     bool      `json:"enabled"`     // 是否允许角色成员访问后台。
	Permissions []string  `json:"permissions"` // 显式授予的操作权限。
	UserCount   int       `json:"user_count"`  // 使用该角色的未删除用户数。
	CreatedAt   time.Time `json:"created_at"`  // 创建时间。
	UpdatedAt   time.Time `json:"updated_at"`  // 更新时间。
}

// AdminRoleInput 包含管理员可配置的角色属性。
type AdminRoleInput struct {
	Name        string   `json:"name" binding:"required,max=64"` // 角色名称。
	Description string   `json:"description" binding:"max=1000"` // 角色说明。
	Enabled     bool     `json:"enabled"`                        // 是否启用角色。
	Permissions []string `json:"permissions" binding:"max=512"`  // 完整的角色权限集合。
}

// AdminRoleService 管理角色定义，不缓存用于鉴权的角色权限。
type AdminRoleService struct {
	client *dbent.Client // 应用共用的数据库客户端。
}

// NewAdminRoleService 创建后台角色服务。
func NewAdminRoleService(client *dbent.Client) *AdminRoleService {
	return &AdminRoleService{client: client}
}

// AdminRoleFromEntity 将预加载的角色转换为对外角色信息。
func AdminRoleFromEntity(role *dbent.AdminRole) *AdminRole {
	if role == nil {
		return nil
	}
	return &AdminRole{ID: role.ID, Name: role.Name, Description: role.Description,
		Enabled: role.Enabled, Permissions: role.Permissions, CreatedAt: role.CreatedAt, UpdatedAt: role.UpdatedAt}
}

// List 分页读取角色，并按整页角色批量统计用户人数。
func (s *AdminRoleService) List(ctx context.Context, page, size int, search string) ([]AdminRole, int, error) {
	query := s.client.AdminRole.Query()
	if search = strings.TrimSpace(search); search != "" {
		query.Where(adminrole.Or(adminrole.NameContainsFold(search), adminrole.DescriptionContainsFold(search)))
	}
	total, err := query.Clone().Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	roles, err := query.Order(dbent.Asc(adminrole.FieldID)).Offset((page - 1) * size).Limit(size).All(ctx)
	if err != nil {
		return nil, 0, err
	}
	items := make([]AdminRole, 0, len(roles))
	ids := make([]int64, 0, len(roles))
	for _, role := range roles {
		items = append(items, *AdminRoleFromEntity(role))
		ids = append(ids, role.ID)
	}
	if len(ids) == 0 {
		return items, total, nil
	}
	var counts []struct {
		AdminRoleID int64 `json:"admin_role_id"` // 角色标识。
		Count       int   `json:"count"`         // 角色成员数。
	}
	if err := s.client.User.Query().Where(user.AdminRoleIDIn(ids...)).GroupBy(user.FieldAdminRoleID).
		Aggregate(dbent.Count()).Scan(ctx, &counts); err != nil {
		return nil, 0, err
	}
	byID := make(map[int64]int, len(counts))
	for _, count := range counts {
		byID[count.AdminRoleID] = count.Count
	}
	for i := range items {
		items[i].UserCount = byID[items[i].ID]
	}
	return items, total, nil
}

// Save 校验权限目录，并创建或更新完整角色配置。
func (s *AdminRoleService) Save(ctx context.Context, id int64, input AdminRoleInput) (*AdminRole, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || input.Name == "admin" || input.Name == "user" || input.Name == "管理员" || input.Name == "普通用户" {
		return nil, apperrors.BadRequest("INVALID_ROLE_NAME", "请使用其他角色名称")
	}
	permissions, err := adminaccess.ValidateRolePermissions(input.Permissions)
	if err != nil {
		return nil, apperrors.BadRequest("INVALID_ROLE_PERMISSIONS", err.Error())
	}
	var role *dbent.AdminRole
	if id == 0 {
		role, err = s.client.AdminRole.Create().SetName(input.Name).SetDescription(input.Description).
			SetEnabled(input.Enabled).SetPermissions(permissions).Save(ctx)
	} else {
		role, err = s.client.AdminRole.UpdateOneID(id).SetName(input.Name).SetDescription(input.Description).
			SetEnabled(input.Enabled).SetPermissions(permissions).Save(ctx)
	}
	if dbent.IsNotFound(err) {
		return nil, apperrors.NotFound("ADMIN_ROLE_NOT_FOUND", "角色不存在")
	}
	if dbent.IsConstraintError(err) {
		return nil, apperrors.Conflict("ADMIN_ROLE_NAME_EXISTS", "角色名称已存在")
	}
	if err != nil {
		return nil, fmt.Errorf("save admin role: %w", err)
	}
	return AdminRoleFromEntity(role), nil
}

// Delete 拒绝删除仍被使用的角色，数据库外键同时保护并发角色分配。
func (s *AdminRoleService) Delete(ctx context.Context, id int64) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	count, err := tx.User.Query().Where(user.AdminRoleIDEQ(id)).Count(ctx)
	if err != nil {
		return err
	}
	if count > 0 {
		return apperrors.Conflict("ADMIN_ROLE_IN_USE", "请先为角色成员选择其他角色")
	}
	if _, err := tx.User.Update().Where(user.AdminRoleIDEQ(id), user.DeletedAtNotNil()).
		ClearAdminRoleID().Save(mixins.SkipSoftDelete(ctx)); err != nil {
		return err
	}
	if err := tx.AdminRole.DeleteOneID(id).Exec(ctx); err != nil {
		if dbent.IsNotFound(err) {
			return apperrors.NotFound("ADMIN_ROLE_NOT_FOUND", "角色不存在")
		}
		if dbent.IsConstraintError(err) {
			return apperrors.Conflict("ADMIN_ROLE_IN_USE", "角色仍被用户使用，请刷新后重试")
		}
		return err
	}
	return tx.Commit()
}

// LockRoleUserMutationTarget 在调用方事务中锁定普通用户，防止检查后被升级为后台成员。
func LockRoleUserMutationTarget(ctx context.Context, client *dbent.Client, id int64) error {
	if adminaccess.RoleFromContext(ctx) == nil {
		return nil
	}
	target, err := client.User.Query().Where(user.IDEQ(id)).Select(user.FieldID, user.FieldRole, user.FieldAdminRoleID).ForUpdate().Only(ctx)
	if err != nil {
		return err
	}
	if target.Role == RoleAdmin || target.AdminRoleID != nil {
		return apperrors.Forbidden("ROLE_PERMISSION_DENIED", "只有管理员可修改后台成员")
	}
	return nil
}
