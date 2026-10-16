package service

import (
	"context"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// ProxyTimezoneService 为代理管理提供独立的时区设置能力。
type ProxyTimezoneService interface {
	SetProxyTimezone(ctx context.Context, id int64, timezone string) (*Proxy, error)
	DetectProxyTimezone(ctx context.Context, id int64, apply bool) (*Proxy, error)
}

// ProxyTimezoneRepository 只更新时区字段，避免覆盖代理的其他配置。
type ProxyTimezoneRepository interface {
	SaveTimezone(ctx context.Context, expected *Proxy, timezone *string, detection *domain.IPGeolocation) (*Proxy, error)
}

// proxyTimezoneCacheUpdater 将时区补充到已有的代理检测缓存。
type proxyTimezoneCacheUpdater interface {
	SetProxyTimezone(ctx context.Context, proxyID int64, timezone string, detection domain.IPGeolocation, updatedAt time.Time) error
}

// SetProxyTimezone 校验并保存手动时区，保留最近成功的出口检测。
func (s *adminServiceImpl) SetProxyTimezone(ctx context.Context, id int64, timezone string) (*Proxy, error) {
	timezone = strings.TrimSpace(timezone)
	if err := validateGatewayTimezone(timezone); err != nil {
		return nil, err
	}
	proxy, err := s.proxyRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.saveProxyTimezone(ctx, proxy, &timezone, nil)
}

// DetectProxyTimezone 通过指定代理检测出口，只有自动设置才覆盖已保存时区。
func (s *adminServiceImpl) DetectProxyTimezone(ctx context.Context, id int64, apply bool) (*Proxy, error) {
	proxy, err := s.proxyRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	options := httpclient.Options{ProxyURL: proxy.URL(), ValidateResolvedIP: true}
	if s.cfg != nil {
		options.ValidateResolvedIP = s.cfg.Security.URLAllowlist.Enabled
		options.AllowPrivateHosts = s.cfg.Security.URLAllowlist.AllowPrivateHosts
	}
	detection, err := probeGatewayTimezone(ctx, options)
	if err != nil {
		return nil, infraerrors.ServiceUnavailable("PROXY_TIMEZONE_DETECTION_FAILED", "Unable to detect proxy exit timezone; previous settings were retained").WithCause(err)
	}
	var timezone *string
	if apply {
		timezone = &detection.Timezone
	}
	return s.saveProxyTimezone(ctx, proxy, timezone, &detection)
}

// saveProxyTimezone 先持久化，再更新已有缓存；数据库保存失败时不改缓存。
func (s *adminServiceImpl) saveProxyTimezone(ctx context.Context, expected *Proxy, timezone *string, detection *domain.IPGeolocation) (*Proxy, error) {
	repository, ok := s.proxyRepo.(ProxyTimezoneRepository)
	if !ok {
		return nil, infraerrors.ServiceUnavailable("PROXY_TIMEZONE_UNAVAILABLE", "Proxy timezone storage is unavailable")
	}
	proxy, err := repository.SaveTimezone(ctx, expected, timezone, detection)
	if err != nil {
		return nil, err
	}
	s.syncProxyTimezoneCache(ctx, proxy)
	return proxy, nil
}

// syncProxyTimezoneCache 将数据库快照合并到已有代理缓存。
func (s *adminServiceImpl) syncProxyTimezoneCache(ctx context.Context, proxy *Proxy) {
	if cache, ok := s.proxyLatencyCache.(proxyTimezoneCacheUpdater); ok {
		if err := cache.SetProxyTimezone(ctx, proxy.ID, proxy.Timezone, proxy.TimezoneDetection, proxy.UpdatedAt); err != nil {
			logger.LegacyPrintf("service.admin", "Warning: store proxy timezone cache failed: %v", err)
		}
	}
}
