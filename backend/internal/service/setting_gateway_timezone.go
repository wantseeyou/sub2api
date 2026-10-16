package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
)

const (
	gatewayTimezoneKey          = "gateway_timezone"
	gatewayTimezoneDetectionKey = "gateway_timezone_detection"
	gatewayTimezoneCacheTTL     = 60 * time.Second
	gatewayTimezoneProbeTimeout = 4 * time.Second
	gatewayTimezoneProbeURL     = "https://ipwho.is/"
	gatewayTimezoneFallbackURL  = "https://get.geojs.io/v1/ip/geo.json"
	gatewayTimezoneResponseMax  = 64 * 1024
)

// GatewayTimezoneDetection 保存服务器直连公网出口的最近一次成功检测。
type GatewayTimezoneDetection = domain.IPGeolocation

// GatewayTimezoneSettings 独立保存网关展示配置，不改变系统时区。
type GatewayTimezoneSettings struct {
	Timezone  string                   `json:"timezone"`  // 当前设置时区，未设置时使用服务器配置的默认值。
	IsSet     bool                     `json:"is_set"`    // 是否已经显式保存时区。
	Detection GatewayTimezoneDetection `json:"detection"` // 最近成功检测结果，尚未检测时各字段为空。
}

// cachedGatewayTimezoneSettings 保存时区配置及缓存过期时间。
type cachedGatewayTimezoneSettings struct {
	settings  GatewayTimezoneSettings // 缓存的配置快照。
	expiresAt time.Time               // 当前进程重新读取数据库的时间。
}

// GetGatewayTimezoneSettings 读取网关时区配置，最多缓存 60 秒。
func (s *SettingService) GetGatewayTimezoneSettings(ctx context.Context) (GatewayTimezoneSettings, error) {
	s.gatewayTimezoneMu.Lock()
	defer s.gatewayTimezoneMu.Unlock()
	if cached := s.gatewayTimezoneCache; cached != nil && time.Now().Before(cached.expiresAt) {
		return cached.settings, nil
	}
	settings, err := s.readGatewayTimezoneSettings(ctx)
	if err != nil {
		return GatewayTimezoneSettings{}, err
	}
	s.storeGatewayTimezoneCache(settings)
	return settings, nil
}

// readGatewayTimezoneSettings 批量读取两个设置键，供持有互斥锁的调用方使用。
func (s *SettingService) readGatewayTimezoneSettings(ctx context.Context) (GatewayTimezoneSettings, error) {
	values, err := s.settingRepo.GetMultiple(ctx, []string{gatewayTimezoneKey, gatewayTimezoneDetectionKey})
	if err != nil {
		return GatewayTimezoneSettings{}, fmt.Errorf("read gateway timezone settings: %w", err)
	}
	settings := GatewayTimezoneSettings{Timezone: "UTC"}
	if s.cfg != nil && s.cfg.Timezone != "" {
		settings.Timezone = s.cfg.Timezone
	}
	if value := values[gatewayTimezoneKey]; value != "" {
		settings.Timezone = value
		settings.IsSet = true
	}
	if value := values[gatewayTimezoneDetectionKey]; value != "" {
		if err := json.Unmarshal([]byte(value), &settings.Detection); err != nil {
			return GatewayTimezoneSettings{}, fmt.Errorf("decode gateway timezone detection: %w", err)
		}
	}
	return settings, nil
}

// SetGatewayTimezone 手动保存时区，保留检测结果并立即更新当前进程缓存。
func (s *SettingService) SetGatewayTimezone(ctx context.Context, timezone string) (GatewayTimezoneSettings, error) {
	timezone = strings.TrimSpace(timezone)
	if err := validateGatewayTimezone(timezone); err != nil {
		return GatewayTimezoneSettings{}, err
	}
	s.gatewayTimezoneMu.Lock()
	defer s.gatewayTimezoneMu.Unlock()
	settings, err := s.readGatewayTimezoneSettings(ctx)
	if err != nil {
		return GatewayTimezoneSettings{}, err
	}
	if err := s.settingRepo.Set(ctx, gatewayTimezoneKey, timezone); err != nil {
		return GatewayTimezoneSettings{}, fmt.Errorf("save gateway timezone: %w", err)
	}
	settings.Timezone, settings.IsSet = timezone, true
	s.storeGatewayTimezoneCache(settings)
	return settings, nil
}

// DetectGatewayTimezone 刷新出口检测；自动设置时原子保存检测结果和设置时区。
func (s *SettingService) DetectGatewayTimezone(ctx context.Context, apply bool) (GatewayTimezoneSettings, error) {
	detection, err := probeGatewayTimezone(ctx, httpclient.Options{})
	if err != nil {
		return GatewayTimezoneSettings{}, infraerrors.ServiceUnavailable(
			"GATEWAY_TIMEZONE_DETECTION_FAILED", "Unable to detect server exit timezone; previous settings were retained",
		).WithCause(err)
	}
	encoded, err := json.Marshal(detection)
	if err != nil {
		return GatewayTimezoneSettings{}, fmt.Errorf("encode gateway timezone detection: %w", err)
	}
	s.gatewayTimezoneMu.Lock()
	defer s.gatewayTimezoneMu.Unlock()
	settings, err := s.readGatewayTimezoneSettings(ctx)
	if err != nil {
		return GatewayTimezoneSettings{}, err
	}
	values := map[string]string{gatewayTimezoneDetectionKey: string(encoded)}
	if apply {
		values[gatewayTimezoneKey] = detection.Timezone
	}
	if err := s.settingRepo.SetMultiple(ctx, values); err != nil {
		return GatewayTimezoneSettings{}, fmt.Errorf("save gateway timezone detection: %w", err)
	}
	settings.Detection = detection
	if apply {
		settings.Timezone, settings.IsSet = detection.Timezone, true
	}
	s.storeGatewayTimezoneCache(settings)
	return settings, nil
}

// storeGatewayTimezoneCache 在持有互斥锁时替换缓存快照。
func (s *SettingService) storeGatewayTimezoneCache(settings GatewayTimezoneSettings) {
	s.gatewayTimezoneCache = &cachedGatewayTimezoneSettings{settings: settings, expiresAt: time.Now().Add(gatewayTimezoneCacheTTL)}
}

// validateGatewayTimezone 拒绝空值、机器本地别名和无法识别的时区。
func validateGatewayTimezone(timezone string) error {
	if timezone != "" && timezone != "Local" {
		if _, err := time.LoadLocation(timezone); err == nil {
			return nil
		}
	}
	return infraerrors.BadRequest("INVALID_GATEWAY_TIMEZONE", "A valid IANA timezone is required")
}

// probeGatewayTimezone 优先查询主接口，失败后尝试一次备用接口。
func probeGatewayTimezone(ctx context.Context, options httpclient.Options) (GatewayTimezoneDetection, error) {
	detection, err := probeIPWhoisTimezone(ctx, options)
	if err == nil {
		return detection, nil
	}
	if ctx.Err() != nil {
		return GatewayTimezoneDetection{}, ctx.Err()
	}
	var fallback GatewayTimezoneDetection
	fallbackErr := requestGatewayTimezoneJSON(ctx, options, gatewayTimezoneFallbackURL, &fallback)
	if fallbackErr == nil {
		if net.ParseIP(fallback.IP) == nil || validateGatewayTimezone(fallback.Timezone) != nil {
			fallbackErr = fmt.Errorf("fallback provider returned invalid location data")
		} else {
			fallback.DetectedAt = time.Now().UTC().Format(time.RFC3339)
			return fallback, nil
		}
	}
	return GatewayTimezoneDetection{}, fmt.Errorf("primary timezone lookup: %v; fallback: %w", err, fallbackErr)
}

// requestGatewayTimezoneJSON 按指定出口访问固定查询地址，限制超时和响应大小。
func requestGatewayTimezoneJSON(ctx context.Context, options httpclient.Options, url string, result any) error {
	options.Timeout = gatewayTimezoneProbeTimeout
	client, err := httpclient.GetClient(options)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", "Sub2API")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("timezone provider returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, gatewayTimezoneResponseMax+1))
	if err != nil {
		return err
	}
	if len(body) > gatewayTimezoneResponseMax {
		return fmt.Errorf("timezone provider response is too large")
	}
	return json.Unmarshal(body, result)
}

// probeIPWhoisTimezone 将主接口的响应转换为统一的时区检测结果。
func probeIPWhoisTimezone(ctx context.Context, options httpclient.Options) (GatewayTimezoneDetection, error) {
	var result struct {
		Success     bool   `json:"success"`      // 查询是否成功。
		IP          string `json:"ip"`           // 检测到的公网 IP。
		Country     string `json:"country"`      // 国家名称。
		CountryCode string `json:"country_code"` // 国家代码。
		Region      string `json:"region"`       // 州或省名称。
		City        string `json:"city"`         // 城市名称。
		Timezone    struct {
			ID string `json:"id"` // IANA 时区名称。
		} `json:"timezone"` // 地理时区信息。
	}
	if err := requestGatewayTimezoneJSON(ctx, options, gatewayTimezoneProbeURL, &result); err != nil {
		return GatewayTimezoneDetection{}, err
	}
	if !result.Success || net.ParseIP(result.IP) == nil || validateGatewayTimezone(result.Timezone.ID) != nil {
		return GatewayTimezoneDetection{}, fmt.Errorf("timezone provider returned invalid location data")
	}
	return GatewayTimezoneDetection{
		IP: result.IP, Country: result.Country, CountryCode: result.CountryCode,
		Region: result.Region, City: result.City, Timezone: result.Timezone.ID,
		DetectedAt: time.Now().UTC().Format(time.RFC3339),
	}, nil
}
