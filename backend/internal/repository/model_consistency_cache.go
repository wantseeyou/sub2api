package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const modelConsistencyMaxConcurrentRuns = 4
const modelConsistencyLeaseGrace = 10 * time.Second

// modelConsistencyCache 将诊断运行限制与取消通知共享给各服务实例。
type modelConsistencyCache struct {
	redis *redis.Client // 已有 Redis 客户端。
}

// modelConsistencySubscription 持有单次运行的取消订阅。
type modelConsistencySubscription struct {
	pubsub *redis.PubSub // 仅接收本次运行的取消消息。
}

// NewModelConsistencyCache 创建诊断专用的跨实例协调入口。
func NewModelConsistencyCache(redisClient *redis.Client) service.ModelConsistencyCache {
	return &modelConsistencyCache{redis: redisClient}
}

// modelConsistencyLeaseKeys 将全局计数与账号锁固定在同一 Redis 槽位。
func modelConsistencyLeaseKeys(accountID int64) []string {
	return []string{"model-consistency:{diagnostics}:runs", fmt.Sprintf("model-consistency:{diagnostics}:account:%d", accountID)}
}

// modelConsistencyChannel 为一次运行生成独立取消频道。
func modelConsistencyChannel(accountID int64, runID string) string {
	return fmt.Sprintf("model-consistency:{diagnostics}:cancel:%d:%s", accountID, runID)
}

// Acquire 原子限制全局运行数及同账号运行数，租约在任务截止后自动失效。
func (c *modelConsistencyCache) Acquire(ctx context.Context, accountID int64, runID string, deadline time.Time) (bool, error) {
	result, err := c.redis.Eval(ctx, `
		local now = redis.call('TIME')
		local ms = now[1] * 1000 + math.floor(now[2] / 1000)
		redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', ms)
		if redis.call('EXISTS', KEYS[2]) == 1 or redis.call('ZCARD', KEYS[1]) >= tonumber(ARGV[3]) then return 0 end
		redis.call('SET', KEYS[2], ARGV[1], 'PX', ARGV[2])
		redis.call('ZADD', KEYS[1], ms + tonumber(ARGV[2]), ARGV[1])
		local last = redis.call('ZREVRANGE', KEYS[1], 0, 0, 'WITHSCORES')
		redis.call('PEXPIRE', KEYS[1], tonumber(last[2]) - ms)
		return 1`, modelConsistencyLeaseKeys(accountID), runID, (time.Until(deadline) + modelConsistencyLeaseGrace).Milliseconds(), modelConsistencyMaxConcurrentRuns).Int()
	return result == 1, err
}

// Release 只释放指定运行的租约，不删除该账号后续运行的锁。
func (c *modelConsistencyCache) Release(ctx context.Context, accountID int64, runID string) error {
	return c.redis.Eval(ctx, `if redis.call('GET', KEYS[2]) == ARGV[1] then redis.call('DEL', KEYS[2]) end; redis.call('ZREM', KEYS[1], ARGV[1]); return 1`, modelConsistencyLeaseKeys(accountID), runID).Err()
}

// SubscribeCancel 确认订阅已建立后才允许诊断运行启动。
func (c *modelConsistencyCache) SubscribeCancel(ctx context.Context, accountID int64, runID string) (service.ModelConsistencyCancelSubscription, error) {
	pubsub := c.redis.Subscribe(ctx, modelConsistencyChannel(accountID, runID))
	if _, err := pubsub.Receive(ctx); err != nil {
		_ = pubsub.Close()
		return nil, err
	}
	return &modelConsistencySubscription{pubsub: pubsub}, nil
}

// PublishCancel 通知实际持有运行的实例取消上下游请求。
func (c *modelConsistencyCache) PublishCancel(ctx context.Context, accountID int64, runID string) error {
	return c.redis.Publish(ctx, modelConsistencyChannel(accountID, runID), "cancel").Err()
}

// Wait 等待取消消息或订阅故障，由调用方立即停止诊断。
func (s *modelConsistencySubscription) Wait(ctx context.Context) error {
	_, err := s.pubsub.ReceiveMessage(ctx)
	return err
}

// Close 关闭专用订阅并释放连接。
func (s *modelConsistencySubscription) Close() error {
	return s.pubsub.Close()
}
