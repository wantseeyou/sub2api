package handler

import (
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// GetLeaderboard 在开关启用时向已登录的普通用户提供用量排行榜。
func (h *UsageHandler) GetLeaderboard(c *gin.Context) {
	if _, ok := middleware.GetAuthSubjectFromContext(c); !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	role, _ := middleware.GetUserRoleFromContext(c)
	if role != service.RoleUser {
		response.Forbidden(c, "Usage leaderboard is only available to regular users")
		return
	}
	enabled, err := h.settingService.IsUsageLeaderboardEnabled(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if !enabled {
		response.NotFound(c, "Usage leaderboard is disabled")
		return
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if err != nil {
		response.BadRequest(c, "Invalid limit, use 50 or 100")
		return
	}
	result, err := h.usageService.GetLeaderboard(c.Request.Context(), c.DefaultQuery("period", "today"), c.DefaultQuery("sort_by", "total_tokens"), limit)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	response.Success(c, result)
}
