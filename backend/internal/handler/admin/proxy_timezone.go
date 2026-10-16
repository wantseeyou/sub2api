package admin

import (
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/gin-gonic/gin"
)

// SetTimezone 保存单个代理的手动时区。
func (h *ProxyHandler) SetTimezone(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid proxy ID")
		return
	}
	var request struct {
		Timezone string `json:"timezone" binding:"required"` // 要保存的 IANA 时区。
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		response.BadRequest(c, "Invalid timezone request")
		return
	}
	if h.timezoneService == nil {
		response.Error(c, 503, "Proxy timezone service is unavailable")
		return
	}
	proxy, err := h.timezoneService.SetProxyTimezone(c.Request.Context(), id, request.Timezone)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, dto.ProxyFromService(proxy))
}

// DetectTimezone 通过当前代理刷新地区时区，可同时应用到手动设置。
func (h *ProxyHandler) DetectTimezone(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid proxy ID")
		return
	}
	var request struct {
		Apply bool `json:"apply"` // 是否将检测时区同时保存为当前设置。
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		response.BadRequest(c, "Invalid detection request")
		return
	}
	if h.timezoneService == nil {
		response.Error(c, 503, "Proxy timezone service is unavailable")
		return
	}
	proxy, err := h.timezoneService.DetectProxyTimezone(c.Request.Context(), id, request.Apply)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, dto.ProxyFromService(proxy))
}
