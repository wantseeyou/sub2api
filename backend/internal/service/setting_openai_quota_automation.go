package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const SettingKeyOpenAIQuotaAutomation = "openai_quota_automation"

// OpenAIQuotaAutomationSettings 定义 OpenAI OAuth 账号的全局额度自动化规则。
type OpenAIQuotaAutomationSettings struct {
	AutoRecoverEnabled    bool `json:"auto_recover_enabled"`    // 上游 7 日用量归零后自动解除额度停调。
	ExpiryResetEnabled    bool `json:"expiry_reset_enabled"`    // 达到停调阈值后优先使用临期卡。
	ExpiryDays            int  `json:"expiry_days"`             // 重置卡到期前多少天视为临期。
	ThresholdResetEnabled bool `json:"threshold_reset_enabled"` // 达到停调阈值后自动使用有效重置卡。
}

// UsesGlobalReset 判断全局用卡策略是否接管账号级用卡配置。
func (s OpenAIQuotaAutomationSettings) UsesGlobalReset() bool {
	return s.ExpiryResetEnabled || s.ThresholdResetEnabled
}

// defaultOpenAIQuotaAutomationSettings 保持新功能默认关闭、临期天数默认为一天。
func defaultOpenAIQuotaAutomationSettings() OpenAIQuotaAutomationSettings {
	return OpenAIQuotaAutomationSettings{ExpiryDays: 1}
}

// validateOpenAIQuotaAutomationSettings 校验临期天数，避免无效或溢出的时间窗口。
func validateOpenAIQuotaAutomationSettings(settings OpenAIQuotaAutomationSettings) error {
	if settings.ExpiryDays < 1 || settings.ExpiryDays > 365 {
		return infraerrors.BadRequest("OPENAI_QUOTA_AUTOMATION_INVALID", "openai_quota_automation.expiry_days must be between 1 and 365")
	}
	return nil
}

// parseOpenAIQuotaAutomationSettings 读取全局配置并为未配置的安装补齐默认值。
func parseOpenAIQuotaAutomationSettings(raw string) (OpenAIQuotaAutomationSettings, error) {
	settings := defaultOpenAIQuotaAutomationSettings()
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &settings); err != nil {
			return settings, fmt.Errorf("decode OpenAI quota automation: %w", err)
		}
	}
	return settings, validateOpenAIQuotaAutomationSettings(settings)
}

type cachedOpenAIQuotaAutomationSettings struct {
	settings  OpenAIQuotaAutomationSettings // 最近成功读取的全局策略。
	expiresAt time.Time                     // 配置缓存失效时间。
}

// GetOpenAIQuotaAutomationSettings 缓存全局额度策略，避免逐账号重复读取配置。
func (s *SettingService) GetOpenAIQuotaAutomationSettings(ctx context.Context) (OpenAIQuotaAutomationSettings, error) {
	if s == nil || s.settingRepo == nil {
		return defaultOpenAIQuotaAutomationSettings(), nil
	}
	if cached := s.openAIQuotaAutomationCache.Load(); cached != nil && time.Now().Before(cached.expiresAt) {
		return cached.settings, nil
	}
	return s.refreshOpenAIQuotaAutomationSettings(ctx)
}

// refreshOpenAIQuotaAutomationSettings 在核销前重读配置，确保全局开关变更得到复核。
func (s *SettingService) refreshOpenAIQuotaAutomationSettings(ctx context.Context) (OpenAIQuotaAutomationSettings, error) {
	if s == nil || s.settingRepo == nil {
		return defaultOpenAIQuotaAutomationSettings(), nil
	}
	s.openAIQuotaAutomationMu.Lock()
	defer s.openAIQuotaAutomationMu.Unlock()
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyOpenAIQuotaAutomation)
	if err != nil && !errors.Is(err, ErrSettingNotFound) {
		return OpenAIQuotaAutomationSettings{}, fmt.Errorf("read OpenAI quota automation: %w", err)
	}
	settings, err := parseOpenAIQuotaAutomationSettings(raw)
	if err != nil {
		return OpenAIQuotaAutomationSettings{}, err
	}
	s.openAIQuotaAutomationCache.Store(&cachedOpenAIQuotaAutomationSettings{
		settings: settings, expiresAt: time.Now().Add(time.Minute),
	})
	return settings, nil
}
