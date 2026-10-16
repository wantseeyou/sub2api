package repository

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

// 按发布源隔离缓存，避免切换仓库后继续展示原作者的版本信息。
const updateCacheKey = "update:latest:wantseeyou/sub2api"

type updateCache struct {
	rdb *redis.Client
}

func NewUpdateCache(rdb *redis.Client) service.UpdateCache {
	return &updateCache{rdb: rdb}
}

func (c *updateCache) GetUpdateInfo(ctx context.Context) (string, error) {
	return c.rdb.Get(ctx, updateCacheKey).Result()
}

func (c *updateCache) SetUpdateInfo(ctx context.Context, data string, ttl time.Duration) error {
	return c.rdb.Set(ctx, updateCacheKey, data, ttl).Err()
}
