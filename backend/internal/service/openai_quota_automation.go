package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"reflect"
	"strconv"
	"time"
)

const OpenAIQuotaZeroRecoveryExtraKey = "codex_7d_zero_recovery"

type openAIQuotaAutomationRepository interface {
	ListOpenAIQuotaAutomationCandidateIDs(context.Context, int64, int, bool) ([]int64, error)
	UpdateOpenAIQuotaAutomationSnapshot(context.Context, *Account, map[string]any, bool) (bool, error)
}

// scanQuotaAutomationCandidates 以主键分页补偿暂停账号和旧账号级自动用卡任务。
func (s *OpenAIQuotaAutoResetService) scanQuotaAutomationCandidates(ctx context.Context, repo openAIQuotaAutomationRepository) {
	policy, err := s.settings.GetOpenAIQuotaAutomationSettings(ctx)
	if err != nil {
		slog.Warn("openai_quota_automation_settings_failed", "error", err)
		return
	}
	includePaused := policy.AutoRecoverEnabled || policy.UsesGlobalReset() ||
		(s.quotaNotifier != nil && (s.quotaNotifier.isAccountQuotaNotifyEnabled(ctx) || s.quotaNotifier.xtuisPush.IsAccountQuotaEnabled(ctx)))
	var afterID int64
	for ctx.Err() == nil {
		ids, err := repo.ListOpenAIQuotaAutomationCandidateIDs(ctx, afterID, openAIAutoResetBatchSize, includePaused)
		if err != nil {
			slog.Warn("openai_quota_automation_scan_failed", "error", err)
			return
		}
		for _, id := range ids {
			s.Notify(id)
		}
		if len(ids) < openAIAutoResetBatchSize {
			return
		}
		afterID = ids[len(ids)-1]
	}
}

// isOpenAIQuotaThresholdPaused 只接管正常启用母账号的额度阈值停调。
func isOpenAIQuotaThresholdPaused(account *Account, now time.Time) bool {
	return isOpenAIAutoResetCreditAccount(account) && account.IsActive() && account.Schedulable &&
		(!account.AutoPauseOnExpired || account.ExpiresAt == nil || account.ExpiresAt.After(now)) &&
		account.TempUnschedulableUntil != nil && account.TempUnschedulableUntil.After(now) &&
		IsAccountSchedulingThresholdReason(account.TempUnschedulableReason)
}

// evaluateGlobalQuotaAutomation 先确认真实用量回落或归零，再按统一停调阈值决定是否用卡。
func (s *OpenAIQuotaAutoResetService) evaluateGlobalQuotaAutomation(ctx context.Context, account *Account, policy OpenAIQuotaAutomationSettings) (bool, error) {
	handled := policy.UsesGlobalReset()
	repo, ok := s.accountRepo.(openAIQuotaAutomationRepository)
	if !ok {
		return true, fmt.Errorf("account repository does not support global quota automation")
	}
	usage, err := s.quota.QueryUsage(ctx, account.ID)
	if err != nil {
		return true, err
	}
	if err := validateGlobalOpenAIQuotaUsage(usage); err != nil {
		return true, err
	}
	now := time.Now()
	updates := buildGlobalOpenAIQuotaUpdates(usage, now)
	// 核销或恢复之前重新读取开关，避免查询期间关闭的任务继续操作。
	policy, err = s.settings.refreshOpenAIQuotaAutomationSettings(ctx)
	if err != nil {
		return true, err
	}
	if policy.AutoRecoverEnabled {
		cycle, recover := openAIQuotaZeroRecoveryCycle(account, usage, now)
		if openAIQuotaPauseUsageDecreased(account, usage) || recover {
			if cycle != "" {
				updates[OpenAIQuotaZeroRecoveryExtraKey] = cycle
			}
			_, err := s.recoverGlobalOpenAIQuotaPause(ctx, account, updates)
			return true, err
		}
	}
	changed, err := repo.UpdateOpenAIQuotaAutomationSnapshot(ctx, account, updates, false)
	if err != nil || !changed {
		return true, err
	}
	if err := s.quota.CacheResetCreditsSnapshot(ctx, account.ID, usage.RateLimitResetCredits); err != nil {
		return true, err
	}
	if !policy.UsesGlobalReset() {
		return handled, nil
	}
	account, err = s.accountRepo.GetByID(ctx, account.ID)
	if err != nil || !isOpenAIQuotaThresholdPaused(account, time.Now()) {
		return true, err
	}
	decision := s.evaluateConfirmedQuotaThreshold(ctx, account, usage, now)
	previous := openAIAutoResetStateFromExtra(account.Extra)
	canReplay := previous != nil && previous.AttemptCreditHash != "" &&
		(previous.AttemptCycleHash == shortOpenAIAutoResetHash("pause:"+account.TempUnschedulableReason) || hasPendingOpenAIAutoResetAttempt(previous))
	if !decision.ShouldPause && !canReplay {
		return true, nil
	}
	if usage.RateLimitResetCredits == nil {
		return true, fmt.Errorf("OpenAI reset credit details are unavailable")
	}
	checking := &OpenAIAutoResetCreditState{
		Status: OpenAIAutoResetStatusChecking, TriggerWindow: decision.Window,
		AvailableCount: usage.RateLimitResetCredits.AvailableCount, CheckedAt: now.UTC().Format(time.RFC3339),
	}
	copyOpenAIAutoResetAttempt(checking, previous)
	if hasPendingOpenAIAutoResetAttempt(previous) {
		checking.Status = previous.Status
	}
	if checking.AvailableCount <= 0 && !canReplay {
		checking.Status = OpenAIAutoResetStatusNoCredit
		return true, s.persistState(ctx, account.ID, checking)
	}
	assessment := openAIAutoResetAssessment{
		triggerWindow: decision.Window, resetReached: decision.ShouldPause, pauseReached: true,
		threshold5h: float64(decision.ThresholdPercent) / 100, threshold7d: float64(decision.ThresholdPercent) / 100,
		utilization5h: readOpenAIQuotaUsedPercent(updates, "5h") / 100,
		utilization7d: readOpenAIQuotaUsedPercent(updates, "7d") / 100,
	}
	return true, s.consumeCredit(ctx, account, usage, checking, assessment, &policy)
}

// validateGlobalOpenAIQuotaUsage 拒绝缺少用量或包含无效百分比的快照。
func validateGlobalOpenAIQuotaUsage(usage *OpenAIQuotaUsage) error {
	if usage == nil || usage.RateLimit == nil {
		return fmt.Errorf("OpenAI quota response has no rate limit data")
	}
	count := 0
	for _, window := range []*OpenAIRateLimitWindow{usage.RateLimit.PrimaryWindow, usage.RateLimit.SecondaryWindow} {
		if window == nil {
			continue
		}
		if !window.usedPercentKnown || math.IsNaN(window.UsedPercent) || math.IsInf(window.UsedPercent, 0) || window.UsedPercent < 0 {
			return fmt.Errorf("OpenAI quota response has a missing or invalid usage percentage")
		}
		count++
	}
	if count == 0 {
		return fmt.Errorf("OpenAI quota response has no usage windows")
	}
	return nil
}

// buildGlobalOpenAIQuotaUpdates 优先采用上游绝对重置时间，避免查询耗时延长窗口。
func buildGlobalOpenAIQuotaUpdates(usage *OpenAIQuotaUsage, now time.Time) map[string]any {
	updates := buildOpenAIAutoResetUsageUpdates(usage, now)
	if usage == nil || usage.RateLimit == nil {
		return updates
	}
	updates[openaiQuotaCreditsKey] = openAICreditsSnapshot{Credits: usage.Credits, FetchedAt: usage.FetchedAt}
	for _, window := range []*OpenAIRateLimitWindow{usage.RateLimit.PrimaryWindow, usage.RateLimit.SecondaryWindow} {
		// 只有受条件更新保护的主动查询可开启下一次归零，晚到的请求响应头不能清标记。
		if window != nil && window.usedPercentKnown && window.LimitWindowSeconds == 7*24*60*60 && window.UsedPercent > 0 {
			updates[OpenAIQuotaZeroRecoveryExtraKey] = nil
		}
		if window == nil || window.ResetAt <= 0 {
			continue
		}
		name := "5h"
		if window.LimitWindowSeconds > 6*60*60 {
			name = "7d"
		}
		updates["codex_"+name+"_reset_at"] = time.Unix(window.ResetAt, 0).UTC().Format(time.RFC3339)
	}
	return updates
}

// openAIQuotaPauseUsageDecreased 将上游真实用量与本次停调基线比较，避免扫描覆盖缓存后漏掉恢复。
func openAIQuotaPauseUsageDecreased(account *Account, usage *OpenAIQuotaUsage) bool {
	pause, ok := parseTempUnschedReasonPayload(account.TempUnschedulableReason)
	if !ok || pause.Source != AccountSchedulingThresholdReasonSource || pause.UsedPercent <= 0 {
		return false
	}
	var windowSeconds int64
	switch pause.Window {
	case "5h":
		windowSeconds = 5 * 60 * 60
	case "7d":
		windowSeconds = 7 * 24 * 60 * 60
	default:
		return false
	}
	for _, window := range []*OpenAIRateLimitWindow{usage.RateLimit.PrimaryWindow, usage.RateLimit.SecondaryWindow} {
		if window != nil && window.usedPercentKnown && window.LimitWindowSeconds == windowSeconds && window.UsedPercent < pause.UsedPercent {
			return true
		}
	}
	return false
}

// openAIQuotaZeroRecoveryCycle 只使用明确存在的真实七日用量识别尚未处理的归零。
func openAIQuotaZeroRecoveryCycle(account *Account, usage *OpenAIQuotaUsage, now time.Time) (string, bool) {
	if usage == nil || usage.RateLimit == nil {
		return "", false
	}
	for _, window := range []*OpenAIRateLimitWindow{usage.RateLimit.PrimaryWindow, usage.RateLimit.SecondaryWindow} {
		if window == nil || !window.usedPercentKnown || window.LimitWindowSeconds != 7*24*60*60 || window.UsedPercent != 0 {
			continue
		}
		resetAt := window.ResetAt
		if resetAt <= 0 && window.ResetAfterSeconds > 0 && usage.FetchedAt > 0 {
			resetAt = usage.FetchedAt + window.ResetAfterSeconds
		}
		cycle := strconv.FormatInt(resetAt, 10)
		previous, hasPrevious := account.Extra[OpenAIQuotaZeroRecoveryExtraKey]
		if !hasPrevious || previous == nil {
			return cycle, true
		}
		previousReset, err := strconv.ParseInt(fmt.Sprint(previous), 10, 64)
		if err != nil || previousReset <= 0 || resetAt <= previousReset {
			return cycle, false
		}
		// 上游明确返回的新周期可提前恢复；相对时间推算仍须等旧周期结束，避免漂移造成重复恢复。
		return cycle, window.ResetAt > 0 || now.Unix() >= previousReset
	}
	return "", false
}

// hasPendingOpenAIAutoResetAttempt 判断是否仍须沿用原卡及兑换请求处理未确认的结果。
func hasPendingOpenAIAutoResetAttempt(state *OpenAIAutoResetCreditState) bool {
	return state != nil && state.AttemptCycleHash != "" && state.AttemptCreditHash != "" &&
		(state.Status == OpenAIAutoResetStatusResetting || state.Status == OpenAIAutoResetStatusFailed)
}

// globalResetCreditEligible 按全局策略检查卡片是否有效及是否满足临期条件。
func globalResetCreditEligible(policy OpenAIQuotaAutomationSettings, candidate openAIAutoResetCreditCandidate, now time.Time) bool {
	expires, err := time.Parse(time.RFC3339, candidate.ExpiresAt)
	return err == nil && expires.After(now) && (policy.ThresholdResetEnabled ||
		(policy.ExpiryResetEnabled && !expires.After(now.Add(time.Duration(policy.ExpiryDays)*24*time.Hour))))
}

// globalResetStillEligible 防止核销前的账号、停调原因及全局策略变更被忽略。
func (s *OpenAIQuotaAutoResetService) globalResetStillEligible(ctx context.Context, account, observed *Account, usage *OpenAIQuotaUsage, policy OpenAIQuotaAutomationSettings, candidate openAIAutoResetCreditCandidate) bool {
	if !policy.UsesGlobalReset() || !isOpenAIQuotaThresholdPaused(account, time.Now()) ||
		account.TempUnschedulableReason != observed.TempUnschedulableReason ||
		!reflect.DeepEqual(account.Credentials, observed.Credentials) || !reflect.DeepEqual(account.ProxyID, observed.ProxyID) ||
		!globalResetCreditEligible(policy, candidate, time.Now()) {
		return false
	}
	return s.evaluateConfirmedQuotaThreshold(ctx, account, usage, time.Now()).ShouldPause &&
		EvaluateAccountSchedulingThreshold(account, s.settings.GetAccountSchedulingThresholds(ctx), time.Now()).ShouldPause
}

// evaluateConfirmedQuotaThreshold 仅用本次上游明确返回的窗口判断是否允许消费重置卡。
func (s *OpenAIQuotaAutoResetService) evaluateConfirmedQuotaThreshold(ctx context.Context, account *Account, usage *OpenAIQuotaUsage, now time.Time) AccountSchedulingThresholdDecision {
	fresh := *account
	fresh.Extra = buildGlobalOpenAIQuotaUpdates(usage, now)
	return EvaluateAccountSchedulingThreshold(&fresh, s.settings.GetAccountSchedulingThresholds(ctx), now)
}

// finishGlobalQuotaReset 刷新核销后的真实额度，只解除原先匹配的额度停调。
func (s *OpenAIQuotaAutoResetService) finishGlobalQuotaReset(ctx context.Context, observed *Account) OpenAIQuotaResetPostProcessResult {
	result := OpenAIQuotaResetPostProcessResult{}
	account, err := s.accountRepo.GetByID(ctx, observed.ID)
	if err != nil || account == nil {
		result.WarningCode = OpenAIQuotaResetWarningAccountRefreshFailed
		return result
	}
	usage, err := s.quota.QueryUsage(ctx, account.ID)
	if err != nil || validateGlobalOpenAIQuotaUsage(usage) != nil {
		result.WarningCode = OpenAIQuotaResetWarningCacheRefreshFailed
		return result
	}
	result.Quota = usage
	if err := s.quota.CacheResetCreditsSnapshot(ctx, account.ID, usage.RateLimitResetCredits); err != nil {
		result.WarningCode = OpenAIQuotaResetWarningCacheRefreshFailed
		return result
	}
	result.CacheRefreshed = true
	if !isOpenAIQuotaThresholdPaused(account, time.Now()) || account.TempUnschedulableReason != observed.TempUnschedulableReason {
		// 新出现的故障或人工操作由其自己的流程处理。
		result.AccountStateRecovered = true
		return result
	}
	updates := buildGlobalOpenAIQuotaUpdates(usage, time.Now())
	for _, window := range []string{"5h", "7d"} {
		key := "codex_" + window + "_used_percent"
		if account.Extra[key] != nil && updates[key] == nil {
			result.WarningCode = OpenAIQuotaResetWarningCacheRefreshFailed
			return result
		}
	}
	if s.evaluateConfirmedQuotaThreshold(ctx, account, usage, time.Now()).ShouldPause {
		result.WarningCode = OpenAIQuotaResetWarningAccountRecoveryFailed
		return result
	}
	changed, err := s.recoverGlobalOpenAIQuotaPause(ctx, account, updates)
	if err != nil || !changed {
		result.WarningCode = OpenAIQuotaResetWarningAccountRecoveryFailed
		return result
	}
	result.AccountStateRecovered = true
	return result
}

// globalOpenAIQuotaResetActive 在调度热路径只读缓存，避免旧用卡阈值覆盖全局规则。
func globalOpenAIQuotaResetActive() bool {
	openAIAutoResetNotifierRegistry.RLock()
	service := openAIAutoResetNotifierRegistry.service
	openAIAutoResetNotifierRegistry.RUnlock()
	if service == nil || service.settings == nil {
		return false
	}
	cached := service.settings.openAIQuotaAutomationCache.Load()
	return cached != nil && cached.settings.UsesGlobalReset()
}

// UnmarshalJSON 区分七日用量真实归零与上游遗漏 used_percent 字段。
func (w *OpenAIRateLimitWindow) UnmarshalJSON(data []byte) error {
	type windowAlias OpenAIRateLimitWindow
	var decoded struct {
		*windowAlias
		UsedPercent *float64 `json:"used_percent"` // 明确提供的上游用量。
	}
	decoded.windowAlias = (*windowAlias)(w)
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	w.usedPercentKnown = decoded.UsedPercent != nil
	if decoded.UsedPercent != nil {
		w.UsedPercent = *decoded.UsedPercent
	}
	return nil
}
