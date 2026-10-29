package groupview

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

const (
	maxPageSize  = 100
	maxViewRange = 366 * 24 * time.Hour
)

type Handler struct {
	store *Store // 独立分组查看存储。
}

// NewHandler 创建只依赖授权及安全查询的处理器。
func NewHandler(store *Store) *Handler { return &Handler{store: store} }

// Access 返回当前用户的有效授权，仅用于界面入口与分组选择。
func (h *Handler) Access(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	groups, err := h.store.Grants(c.Request.Context(), subject.UserID)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, groups)
}

// Accounts 读取授权组的账号概况。
func (h *Handler) Accounts(c *gin.Context) { h.read(c, "accounts") }

// People 读取授权组的人员用量。
func (h *Handler) People(c *gin.Context) { h.read(c, "people") }

// Usage 读取授权组的使用明细。
func (h *Handler) Usage(c *gin.Context) { h.read(c, "usage") }

// Summary 读取授权组的汇总和趋势。
func (h *Handler) Summary(c *gin.Context) { h.read(c, "summary") }

// Options 读取授权组和所选时段中的分页筛选候选。
func (h *Handler) Options(c *gin.Context) { h.read(c, "options") }

// read 先校验最新授权，再将同一范围显式传给每类读取查询。
func (h *Handler) read(c *gin.Context, kind string) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	grants, err := h.store.Grants(c.Request.Context(), subject.UserID)
	if response.ErrorFrom(c, err) {
		return
	}
	selected, err := parseGroupIDs(c.Query("group_ids"))
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	scope, err := Scope(grants, selected)
	if response.ErrorFrom(c, err) {
		return
	}
	f, err := parseFilter(c, kind != "accounts")
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	f.Groups = scope
	if kind == "summary" {
		data, err := h.store.Summary(c.Request.Context(), f)
		if response.ErrorFrom(c, err) {
			return
		}
		response.Success(c, data)
		return
	}
	switch kind {
	case "accounts":
		items, total, err := h.store.Accounts(c.Request.Context(), f)
		if response.ErrorFrom(c, err) {
			return
		}
		response.Paginated(c, items, total, f.Page, f.PageSize)
	case "people":
		items, total, err := h.store.People(c.Request.Context(), f)
		if response.ErrorFrom(c, err) {
			return
		}
		response.Paginated(c, items, total, f.Page, f.PageSize)
	case "usage":
		items, total, err := h.store.Usage(c.Request.Context(), f)
		if response.ErrorFrom(c, err) {
			return
		}
		response.Paginated(c, items, total, f.Page, f.PageSize)
	case "options":
		option := c.Query("kind")
		if option != "user" && option != "api_key" && option != "account" && option != "model" {
			response.BadRequest(c, "Invalid option kind")
			return
		}
		items, total, err := h.store.Options(c.Request.Context(), f, option, c.Query("option_search"))
		if response.ErrorFrom(c, err) {
			return
		}
		response.Paginated(c, items, total, f.Page, f.PageSize)
	}
}

// parseGroupIDs 严格解析显式分组筛选，非法输入不会退回全部授权。
func parseGroupIDs(raw string) ([]int64, error) {
	if raw == "" {
		return nil, nil
	}
	ids := make([]int64, 0)
	for _, part := range strings.Split(raw, ",") {
		id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("invalid group_ids")
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// parseFilter 验证分页、时间和资源条件，拒绝无法解释的筛选。
func parseFilter(c *gin.Context, timed bool) (Filter, error) {
	f := Filter{Page: 1, PageSize: 20, Timezone: "UTC", Model: c.Query("model"), Search: c.Query("search"),
		Platform: c.Query("platform"), Type: c.Query("type"), Status: c.Query("status"), Sort: c.Query("sort_by"), Desc: c.DefaultQuery("sort_order", "desc") == "desc", RequestType: c.Query("request_type")}
	for _, target := range []struct {
		name  string // 资源筛选参数名。
		value *int64 // 解析结果的写入位置。
	}{{"user_id", &f.UserID}, {"api_key_id", &f.APIKeyID}, {"account_id", &f.AccountID}} {
		if raw, exists := c.GetQuery(target.name); exists {
			value, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || value <= 0 {
				return f, fmt.Errorf("invalid %s", target.name)
			}
			*target.value = value
		}
	}
	for _, target := range []struct {
		name  string // 分页参数名。
		value *int   // 解析结果的写入位置。
		max   int    // 该参数的上限。
	}{{"page", &f.Page, 1000000}, {"page_size", &f.PageSize, maxPageSize}} {
		if raw, exists := c.GetQuery(target.name); exists {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 1 || value > target.max {
				return f, fmt.Errorf("invalid %s", target.name)
			}
			*target.value = value
		}
	}
	if order := c.Query("sort_order"); order != "" && order != "asc" && order != "desc" {
		return f, fmt.Errorf("invalid sort_order")
	}
	switch f.RequestType {
	case "", "sync", "stream", "ws_v2", "cyber", "live":
	default:
		return f, fmt.Errorf("invalid request_type")
	}
	if timed {
		var err error
		f.Start, err = time.Parse(time.RFC3339, c.Query("start_time"))
		if err != nil {
			return f, fmt.Errorf("start_time must use RFC3339")
		}
		f.End, err = time.Parse(time.RFC3339, c.Query("end_time"))
		if err != nil || !f.End.After(f.Start) || f.End.Sub(f.Start) > maxViewRange {
			return f, fmt.Errorf("invalid end_time or time range exceeds 366 days")
		}
	}
	if tz := c.Query("timezone"); tz != "" {
		if _, err := time.LoadLocation(tz); err != nil {
			return f, fmt.Errorf("invalid timezone")
		}
		f.Timezone = tz
	}
	return f, nil
}

// adminUser 验证现有管理员身份，受限管理密钥不能管理查看授权。
func adminUser(c *gin.Context) (int64, bool) {
	role, ok := middleware.GetUserRoleFromContext(c)
	if !ok || role != "admin" || c.GetString("auth_method") != "jwt" {
		response.Forbidden(c, "Administrator login required")
		return 0, false
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid user ID")
		return 0, false
	}
	return id, true
}

// GetGrants 供管理员读取目标用户的查看分组。
func (h *Handler) GetGrants(c *gin.Context) {
	id, ok := adminUser(c)
	if !ok {
		return
	}
	groups, err := h.store.Grants(c.Request.Context(), id)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, groups)
}

// SetGrants 供管理员原子替换目标用户的查看分组，空数组表示全部撤销。
func (h *Handler) SetGrants(c *gin.Context) {
	id, ok := adminUser(c)
	if !ok {
		return
	}
	var req struct {
		GroupIDs *[]int64 `json:"group_ids"` /* 完整的目标分组集合。 */
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.GroupIDs == nil || len(*req.GroupIDs) > 1000 {
		response.BadRequest(c, "group_ids must be an array with at most 1000 groups")
		return
	}
	for _, id := range *req.GroupIDs {
		if id <= 0 {
			response.BadRequest(c, "Invalid group ID")
			return
		}
	}
	if response.ErrorFrom(c, h.store.ReplaceGrants(c.Request.Context(), id, *req.GroupIDs)) {
		return
	}
	response.Success(c, gin.H{"group_ids": *req.GroupIDs})
}
