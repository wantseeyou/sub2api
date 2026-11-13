package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	SettingKeyUpdateProxyID = "update_proxy_id"
	UpdateProxyUseConfig    = int64(0)  // 沿用启动配置。
	UpdateProxyDirect       = int64(-1) // 服务器本机直连。
)

// GetUpdateProxyURL 读取当前更新线路，仅沿用启动配置时保留其直连回退策略。
func (s *SettingService) GetUpdateProxyURL(ctx context.Context) (string, bool, error) {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyUpdateProxyID)
	if err != nil && !errors.Is(err, ErrSettingNotFound) {
		return "", false, fmt.Errorf("read update proxy setting: %w", err)
	}
	id := UpdateProxyUseConfig
	if strings.TrimSpace(value) != "" {
		id, err = strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil {
			return "", false, infraerrors.BadRequest("INVALID_UPDATE_PROXY", "invalid update proxy setting")
		}
	}
	proxyURL, err := s.resolveUpdateProxyURL(ctx, id)
	allowDirectOnError := id == UpdateProxyUseConfig && s.cfg != nil && s.cfg.Security.ProxyFallback.AllowDirectOnError
	return proxyURL, allowDirectOnError, err
}

// resolveUpdateProxyURL 校验更新代理并解析地址，不对失效代理静默回退。
func (s *SettingService) resolveUpdateProxyURL(ctx context.Context, id int64) (string, error) {
	switch id {
	case UpdateProxyUseConfig:
		if s.cfg != nil {
			return s.cfg.Update.ProxyURL, nil
		}
		return "", nil
	case UpdateProxyDirect:
		return "", nil
	}
	if id < 0 {
		return "", infraerrors.BadRequest("INVALID_UPDATE_PROXY", "invalid update proxy ID")
	}
	if s.proxyRepo == nil {
		return "", fmt.Errorf("update proxy repository is unavailable")
	}
	proxy, err := s.proxyRepo.GetByID(ctx, id)
	if errors.Is(err, ErrProxyNotFound) {
		return "", infraerrors.BadRequest("INVALID_UPDATE_PROXY", "selected update proxy no longer exists")
	}
	if err != nil {
		return "", fmt.Errorf("load update proxy: %w", err)
	}
	if !proxy.IsActive() || proxy.IsExpired(time.Now()) {
		return "", infraerrors.BadRequest("INVALID_UPDATE_PROXY", "selected update proxy is inactive or expired")
	}
	return proxy.URL(), nil
}
