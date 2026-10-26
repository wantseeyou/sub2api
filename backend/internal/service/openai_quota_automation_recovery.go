package service

import (
	"context"
	"fmt"
	"log/slog"
)

// clearOpenAIQuotaPauseCache 清除匹配的额度冷却缓存，不清除其他错误或模型限流。
func (s *RateLimitService) clearOpenAIQuotaPauseCache(ctx context.Context, account *Account) error {
	if account == nil || account.TempUnschedulableUntil == nil || !IsAccountSchedulingThresholdReason(account.TempUnschedulableReason) {
		return nil
	}
	if s.tempUnschedCache != nil {
		cache, ok := s.tempUnschedCache.(interface {
			DeleteTempUnschedIfMatch(context.Context, int64, *TempUnschedState) error
		})
		if !ok {
			return fmt.Errorf("temporary scheduling cache does not support conditional recovery")
		}
		expected := tempUnschedStateFromStoredReason(account.TempUnschedulableReason, account.TempUnschedulableUntil.Unix())
		if err := cache.DeleteTempUnschedIfMatch(ctx, account.ID, expected); err != nil {
			return fmt.Errorf("clear OpenAI quota pause cache: %w", err)
		}
	}
	current, err := s.accountRepo.GetByID(ctx, account.ID)
	if err != nil {
		return err
	}
	if current != nil && current.TempUnschedulableUntil == nil {
		s.notifyAccountSchedulingBlockCleared(account.ID)
	}
	return nil
}

// recoverGlobalOpenAIQuotaPause 通过条件更新只恢复本次观察到的额度停调。
func (s *OpenAIQuotaAutoResetService) recoverGlobalOpenAIQuotaPause(ctx context.Context, account *Account, updates map[string]any) (bool, error) {
	repo, ok := s.accountRepo.(openAIQuotaAutomationRepository)
	if !ok {
		return false, fmt.Errorf("account repository does not support conditional quota recovery")
	}
	cleaner, ok := s.recoverer.(interface {
		clearOpenAIQuotaPauseCache(context.Context, *Account) error
	})
	if !ok {
		return false, fmt.Errorf("quota recovery does not support conditional cache cleanup")
	}
	// 先检查并清理缓存；Redis 不可用时保留数据库停调，供下一次扫描重试。
	if err := cleaner.clearOpenAIQuotaPauseCache(ctx, account); err != nil {
		return false, err
	}
	changed, err := repo.UpdateOpenAIQuotaAutomationSnapshot(ctx, account, updates, true)
	if err != nil || !changed {
		return changed, err
	}
	if err := cleaner.clearOpenAIQuotaPauseCache(ctx, account); err != nil {
		slog.Warn("openai_quota_recovery_cache_sync_failed", "account_id", account.ID, "error", err)
		return true, err
	}
	slog.Info("openai_quota_pause_recovered", "account_id", account.ID)
	return true, nil
}
