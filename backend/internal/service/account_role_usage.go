package service

import (
	"context"
	"time"
)

// GetStoredUsageBatch 一次查询账号，仅使用已保存的快照或内存缓存构建只读用量。
func (s *AccountUsageService) GetStoredUsageBatch(ctx context.Context, ids []int64) (map[int64]*UsageInfo, map[int64]string, error) {
	accounts, err := s.accountRepo.GetByIDs(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
	result := make(map[int64]*UsageInfo, len(accounts))
	failures := make(map[int64]string)
	for _, account := range accounts {
		if account == nil {
			continue
		}
		now := time.Now()
		info := &UsageInfo{Source: "passive"}
		if supportsAnthropicPassiveUsage(account) {
			info = s.estimateSetupTokenUsage(account)
			info.Source = "passive"
			info.SevenDay = buildPassiveUsageWindow(account.Extra, "passive_usage_7d_utilization", "passive_usage_7d_reset")
			info.SevenDayFable = buildPassiveUsageWindow(account.Extra, "passive_usage_7d_oi_utilization", "passive_usage_7d_oi_reset")
			if raw, ok := account.Extra["passive_usage_sampled_at"].(string); ok {
				if sampledAt, err := time.Parse(time.RFC3339, raw); err == nil {
					info.UpdatedAt = &sampledAt
				}
			}
		} else if account.Platform == PlatformOpenAI {
			applyExtraToUsage(info, account.Extra, now)
		} else if account.Platform == PlatformGrok && s.grokQuotaFetcher != nil {
			info = s.grokQuotaFetcher.BuildUsageInfo(account)
		} else if account.Platform == PlatformAntigravity && s.cache != nil {
			if cached, ok := s.cache.antigravityCache.Load(account.ID); ok {
				if snapshot, ok := cached.(*antigravityUsageCache); ok && snapshot.usageInfo != nil {
					copied := *snapshot.usageInfo
					info = &copied
					info.Source = "passive"
				}
			}
		}
		enrichUsageWithAccountError(info, account)
		result[account.ID] = info
	}
	for _, id := range ids {
		if _, ok := result[id]; !ok {
			failures[id] = "Account not found"
		}
	}
	return result, failures, nil
}
