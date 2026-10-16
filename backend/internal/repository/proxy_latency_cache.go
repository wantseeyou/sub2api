package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const proxyLatencyKeyPrefix = "proxy:latency:"

// 写入连通性结果时原子保留独立维护的时区字段。
var storeProxyLatencyScript = redis.NewScript(`
local value = cjson.decode(ARGV[1])
local previous = redis.call('GET', KEYS[1])
if previous then
  local ok, cached = pcall(cjson.decode, previous)
  if ok then
    value.timezone = cached.timezone
    value.timezone_detection = cached.timezone_detection
    value.timezone_updated_at = cached.timezone_updated_at
  end
end
return redis.call('SET', KEYS[1], cjson.encode(value))
`)

// 只补充已有缓存，避免把单纯的时区设置误显示为连接检测失败。
var storeProxyTimezoneScript = redis.NewScript(`
local previous = redis.call('GET', KEYS[1])
if not previous then return 0 end
local cached = cjson.decode(previous)
local version = tonumber(ARGV[3])
if cached.timezone_updated_at and tonumber(cached.timezone_updated_at) > version then return 0 end
cached.timezone = ARGV[1]
cached.timezone_detection = cjson.decode(ARGV[2])
cached.timezone_updated_at = ARGV[3]
redis.call('SET', KEYS[1], cjson.encode(cached))
return 1
`)

func proxyLatencyKey(proxyID int64) string {
	return fmt.Sprintf("%s%d", proxyLatencyKeyPrefix, proxyID)
}

type proxyLatencyCache struct {
	rdb *redis.Client
}

func NewProxyLatencyCache(rdb *redis.Client) service.ProxyLatencyCache {
	return &proxyLatencyCache{rdb: rdb}
}

func (c *proxyLatencyCache) GetProxyLatencies(ctx context.Context, proxyIDs []int64) (map[int64]*service.ProxyLatencyInfo, error) {
	results := make(map[int64]*service.ProxyLatencyInfo)
	if len(proxyIDs) == 0 {
		return results, nil
	}

	keys := make([]string, 0, len(proxyIDs))
	for _, id := range proxyIDs {
		keys = append(keys, proxyLatencyKey(id))
	}

	values, err := c.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return results, err
	}

	for i, raw := range values {
		if raw == nil {
			continue
		}
		var payload []byte
		switch v := raw.(type) {
		case string:
			payload = []byte(v)
		case []byte:
			payload = v
		default:
			continue
		}
		var info service.ProxyLatencyInfo
		if err := json.Unmarshal(payload, &info); err != nil {
			continue
		}
		results[proxyIDs[i]] = &info
	}

	return results, nil
}

func (c *proxyLatencyCache) SetProxyLatency(ctx context.Context, proxyID int64, info *service.ProxyLatencyInfo) error {
	if info == nil {
		return nil
	}
	payload, err := json.Marshal(info)
	if err != nil {
		return err
	}
	return storeProxyLatencyScript.Run(ctx, c.rdb, []string{proxyLatencyKey(proxyID)}, payload).Err()
}

// SetProxyTimezone 将已持久化的时区原子合并到现有缓存，保留延迟和质量检测结果。
func (c *proxyLatencyCache) SetProxyTimezone(ctx context.Context, proxyID int64, timezone string, detection domain.IPGeolocation, updatedAt time.Time) error {
	payload, err := json.Marshal(detection)
	if err != nil {
		return err
	}
	return storeProxyTimezoneScript.Run(ctx, c.rdb, []string{proxyLatencyKey(proxyID)}, timezone, payload, updatedAt.UnixMicro()).Err()
}
