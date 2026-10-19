package admin

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// SetModelConsistencyService 接入独立的管理员模型一致性诊断服务。
func (h *AccountHandler) SetModelConsistencyService(diagnostics *service.ModelConsistencyService) {
	h.modelConsistency = diagnostics
}

// modelConsistencyAccountID 统一校验诊断服务可用性及账号路径参数。
func (h *AccountHandler) modelConsistencyAccountID(c *gin.Context) (int64, bool) {
	if h.modelConsistency == nil {
		response.Error(c, http.StatusServiceUnavailable, "model_consistency_unavailable")
		return 0, false
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "invalid_account_id")
		return 0, false
	}
	return id, true
}

// writeModelConsistencyError 仅向管理员返回脱敏且稳定的诊断错误码。
func writeModelConsistencyError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrModelConsistencyBusy):
		response.Error(c, http.StatusConflict, "model_consistency_busy")
	case errors.Is(err, service.ErrModelConsistencyUnsupported):
		response.BadRequest(c, "unsupported_account")
	case errors.Is(err, service.ErrModelConsistencyModelsInvalid):
		response.BadRequest(c, "invalid_models")
	case errors.Is(err, service.ErrAccountNotFound):
		response.Error(c, http.StatusNotFound, "account_not_found")
	default:
		response.Error(c, http.StatusServiceUnavailable, "model_consistency_unavailable")
	}
}

// GetModelConsistency 读取指定账号各模型最近的诊断快照。
func (h *AccountHandler) GetModelConsistency(c *gin.Context) {
	id, ok := h.modelConsistencyAccountID(c)
	if !ok {
		return
	}
	view, err := h.modelConsistency.Latest(c.Request.Context(), id)
	if err != nil {
		writeModelConsistencyError(c, err)
		return
	}
	response.Success(c, view)
}

// StartModelConsistency 手动启动一次有界的独立模型一致性检测。
func (h *AccountHandler) StartModelConsistency(c *gin.Context) {
	id, ok := h.modelConsistencyAccountID(c)
	if !ok {
		return
	}
	var request struct {
		Models []string `json:"models" binding:"required,min=1,max=8"` // 本次选择的请求模型。
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		response.BadRequest(c, "invalid_models")
		return
	}
	run, err := h.modelConsistency.Start(c.Request.Context(), id, request.Models)
	if err != nil {
		writeModelConsistencyError(c, err)
		return
	}
	response.Success(c, run)
}

// CancelModelConsistency 跨实例取消指定运行，取消不表示上游一定未计费。
func (h *AccountHandler) CancelModelConsistency(c *gin.Context) {
	id, ok := h.modelConsistencyAccountID(c)
	if !ok {
		return
	}
	cancelled, err := h.modelConsistency.Cancel(c.Request.Context(), id, c.Param("run_id"))
	if err != nil {
		writeModelConsistencyError(c, err)
		return
	}
	response.Success(c, gin.H{"cancelled": cancelled})
}
