package repository

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

var tempUnschedDeleteIfMatchScript = redis.NewScript(`
	local existing = redis.call('GET', KEYS[1])
	if not existing then return 0 end
	if existing == ARGV[1] or existing == ARGV[2] then return redis.call('DEL', KEYS[1]) end
	return 0
`)

// DeleteTempUnschedIfMatch 只删除匹配的旧状态，保留并发写入的新故障。
func (c *tempUnschedCache) DeleteTempUnschedIfMatch(ctx context.Context, accountID int64, expected *service.TempUnschedState) error {
	payload, err := json.Marshal(expected)
	if err != nil {
		return err
	}
	legacyPayload := payload
	// 既有数据库回填路径将缺省 rule_index 解码为 0，额度停调构造路径使用 -1。
	if expected != nil && expected.RuleIndex == -1 {
		legacy := *expected
		legacy.RuleIndex = 0
		legacyPayload, err = json.Marshal(&legacy)
		if err != nil {
			return err
		}
	}
	key := fmt.Sprintf("%s%d", tempUnschedPrefix, accountID)
	return tempUnschedDeleteIfMatchScript.Run(ctx, c.rdb, []string{key}, string(payload), string(legacyPayload)).Err()
}
