package admin

import (
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// AdminRoleHandler 提供仅内置管理员可访问的角色管理。
type AdminRoleHandler struct {
	roles    *service.AdminRoleService // 角色配置服务。
	totp     *service.TotpService      // 二次验证服务。
	users    *service.UserService      // 当前管理员读取服务。
	settings *service.SettingService   // 二次验证设置。
}

// NewAdminRoleHandler 创建角色管理处理器。
func NewAdminRoleHandler(roles *service.AdminRoleService, totp *service.TotpService, users *service.UserService, settings *service.SettingService) *AdminRoleHandler {
	return &AdminRoleHandler{roles: roles, totp: totp, users: users, settings: settings}
}

// RequireAdministrator 拒绝自定义角色和管理 API Key 管理角色配置。
func (h *AdminRoleHandler) RequireAdministrator(c *gin.Context) {
	role, _ := middleware.GetUserRoleFromContext(c)
	if role != service.RoleAdmin || c.GetString("auth_method") != "jwt" || adminaccess.RoleFromContext(c.Request.Context()) != nil {
		response.Forbidden(c, "只有登录的管理员可管理角色")
		c.Abort()
		return
	}
	c.Next()
}

// List 分页列出角色和成员人数。
func (h *AdminRoleHandler) List(c *gin.Context) {
	page, size := response.ParsePagination(c)
	items, total, err := h.roles.List(c.Request.Context(), page, size, c.Query("search"))
	if response.ErrorFrom(c, err) {
		return
	}
	response.Paginated(c, items, int64(total), page, size)
}

// Permissions 返回显式的后台权限目录。
func (h *AdminRoleHandler) Permissions(c *gin.Context) {
	response.Success(c, adminaccess.RolePermissionCatalog())
}

// Save 创建角色或替换指定角色的配置。
func (h *AdminRoleHandler) Save(c *gin.Context) {
	if !middleware.EnforceStepUp(c, h.totp, h.users, h.settings) {
		return
	}
	var input service.AdminRoleInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, "角色配置无效")
		return
	}
	var id int64
	if c.Param("id") != "" {
		var err error
		id, err = strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil || id <= 0 {
			response.BadRequest(c, "角色标识无效")
			return
		}
	}
	item, err := h.roles.Save(c.Request.Context(), id, input)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, item)
}

// Delete 删除没有有效成员的角色。
func (h *AdminRoleHandler) Delete(c *gin.Context) {
	if !middleware.EnforceStepUp(c, h.totp, h.users, h.settings) {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "角色标识无效")
		return
	}
	if response.ErrorFrom(c, h.roles.Delete(c.Request.Context(), id)) {
		return
	}
	response.Success(c, gin.H{"deleted": true})
}
