package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/gin-gonic/gin"
)

// GetGatewayTimezoneSettings 返回网关时区和最近一次出口检测结果。
func (h *SettingHandler) GetGatewayTimezoneSettings(c *gin.Context) {
	settings, err := h.settingService.GetGatewayTimezoneSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, settings)
}

// UpdateGatewayTimezoneSettings 独立保存手动选择的网关时区。
func (h *SettingHandler) UpdateGatewayTimezoneSettings(c *gin.Context) {
	var request struct {
		Timezone string `json:"timezone" binding:"required"` // 手动选择的 IANA 时区。
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		response.BadRequest(c, "A timezone is required")
		return
	}
	settings, err := h.settingService.SetGatewayTimezone(c.Request.Context(), request.Timezone)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, settings)
}

// DetectGatewayTimezone 刷新出口检测，按请求决定是否同时采用检测时区。
func (h *SettingHandler) DetectGatewayTimezone(c *gin.Context) {
	var request struct {
		Apply bool `json:"apply"` // 是否同时保存检测到的时区。
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		response.BadRequest(c, "Invalid detection request")
		return
	}
	settings, err := h.settingService.DetectGatewayTimezone(c.Request.Context(), request.Apply)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, settings)
}
