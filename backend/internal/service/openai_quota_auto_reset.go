package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/google/uuid"
)

const (
	openAIAutoResetScanInterval  = time.Minute
	openAIAutoResetSnapshotTTL   = openAIProbeCacheTTL
	openAIAutoResetBatchSize     = 100
	openAIAutoResetWorkerCount   = 4
	openAIAutoResetQueueCapacity = 1024
	openAIAutoResetAttemptTTL    = 8 * 24 * time.Hour
	openAIAutoResetLeaderLockKey = "jobs:openai-auto-reset-credit"
	// 查询阶段失败后的最短重试间隔。调度热路径和扫描都会触发评估，不设间隔时上游
	// 故障期间同一账号会被十几秒重查一次；用卡阶段的失败不受此限，超时后仍立即用
	// 同一幂等键重试。
	openAIAutoResetQueryFailureRetryAfter = time.Minute
	// 调度热路径每次过滤候选都会评估暂停，对同一账号的通知按此冷却合并；
	// 后台每分钟全量扫描兜底，冷却不会让账号漏检。
	openAIAutoResetSchedulerNotifyCooldown = 30 * time.Second
)

const (
	OpenAIAutoResetStatusChecking  = "checking"
	OpenAIAutoResetStatusAvailable = "available"
	OpenAIAutoResetStatusResetting = "resetting"
	OpenAIAutoResetStatusSuccess   = "success"
	OpenAIAutoResetStatusNoCredit  = "no_credit"
	OpenAIAutoResetStatusFailed    = "failed"
)

// OpenAIAutoResetCreditState 是可返回管理端的脱敏运行态。Attempt* 仅保存不可逆
// 指纹，用于重启后拒绝切换到另一张卡；不会保存卡 ID 或兑换 ID。
type OpenAIAutoResetCreditState struct {
	Status            string `json:"status"`
	TriggerWindow     string `json:"trigger_window,omitempty"`
	AvailableCount    int    `json:"available_count"`
	CheckedAt         string `json:"checked_at,omitempty"`
	LastResultAt      string `json:"last_result_at,omitempty"`
	ErrorCode         string `json:"error_code,omitempty"`
	AttemptCycleHash  string `json:"attempt_cycle_hash,omitempty"`
	AttemptCreditHash string `json:"attempt_credit_hash,omitempty"`
}

type openAIAutoResetQuota interface {
	QueryUsage(ctx context.Context, accountID int64) (*OpenAIQuotaUsage, error)
	CacheResetCreditsSnapshot(ctx context.Context, accountID int64, credits *OpenAIRateLimitResetCredits) error
	CachePostResetSnapshot(ctx context.Context, accountID int64, usage *OpenAIQuotaUsage) error
	ResetCreditTargeted(ctx context.Context, accountID int64, creditID, redeemRequestID string) (*OpenAIQuotaResetResult, error)
}

type openAIAutoResetContextKey struct{}

func withOpenAIAutoResetContext(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, openAIAutoResetContextKey{}, true)
}

func isOpenAIAutoResetContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	value, _ := ctx.Value(openAIAutoResetContextKey{}).(bool)
	return value
}

type openAIAutoResetRecovery interface {
	RecoverAccountState(ctx context.Context, accountID int64, options AccountRecoveryOptions) (*SuccessfulTestRecoveryResult, error)
}

// OpenAIQuotaAutoResetService 通过小型去重队列承接实时信号，并用分钟扫描补偿
// 重启、漏事件和多实例读取；真正消费仍由 PostgreSQL 幂等记录串行化。
type OpenAIQuotaAutoResetService struct {
	accountRepo       AccountRepository
	quota             openAIAutoResetQuota
	recoverer         openAIAutoResetRecovery
	idempotency       *IdempotencyCoordinator
	audit             *AuditLogService
	settings          *SettingService
	leaderLock        LeaderLockCache
	quotaNotifier     *BalanceNotifyService         // 复用管理员通知配置与邮件模板服务。
	notificationQueue *SubscriptionMaintenanceQueue // 有界邮件任务队列。

	ctx     context.Context
	cancel  context.CancelFunc
	queue   chan int64
	pending sync.Map
	owner   string
	start   sync.Once
	stop    sync.Once
	wg      sync.WaitGroup
}

func NewOpenAIQuotaAutoResetService(
	accountRepo AccountRepository,
	quota openAIAutoResetQuota,
	recoverer openAIAutoResetRecovery,
	idempotency *IdempotencyCoordinator,
	audit *AuditLogService,
	settings *SettingService,
	leaderLock LeaderLockCache,
) *OpenAIQuotaAutoResetService {
	ctx, cancel := context.WithCancel(context.Background())
	return &OpenAIQuotaAutoResetService{
		accountRepo: accountRepo,
		quota:       quota,
		recoverer:   recoverer,
		idempotency: idempotency,
		audit:       audit,
		settings:    settings,
		leaderLock:  leaderLock,
		ctx:         ctx,
		cancel:      cancel,
		queue:       make(chan int64, openAIAutoResetQueueCapacity),
		owner:       uuid.NewString(),
	}
}

func (s *OpenAIQuotaAutoResetService) Start() {
	if s == nil || s.accountRepo == nil || s.quota == nil || s.idempotency == nil {
		return
	}
	s.start.Do(func() {
		if s.quotaNotifier != nil {
			s.notificationQueue = NewSubscriptionMaintenanceQueue(2, 100)
		}
		setOpenAIAutoResetNotifier(s)
		for range openAIAutoResetWorkerCount {
			s.wg.Add(1)
			go s.runWorker()
		}
		s.wg.Add(1)
		go s.runScanner()
	})
}

func (s *OpenAIQuotaAutoResetService) Stop() {
	if s == nil {
		return
	}
	s.stop.Do(func() {
		clearOpenAIAutoResetNotifier(s)
		s.cancel()
		s.wg.Wait()
		if s.notificationQueue != nil {
			s.notificationQueue.Stop()
		}
	})
}

// Notify 是请求热路径的非阻塞入口。同一账号尚在队列时只保留一个任务；队列
// 满时丢弃本次信号，分钟扫描仍会补偿，因此不会反向拖慢网关请求。
func (s *OpenAIQuotaAutoResetService) Notify(accountID int64) {
	if s == nil || accountID <= 0 {
		return
	}
	if _, loaded := s.pending.LoadOrStore(accountID, struct{}{}); loaded {
		return
	}
	select {
	case <-s.ctx.Done():
		s.pending.Delete(accountID)
	case s.queue <- accountID:
	default:
		s.pending.Delete(accountID)
		slog.Warn("openai_auto_reset_queue_full", "account_id", accountID)
	}
}

func (s *OpenAIQuotaAutoResetService) runWorker() {
	defer s.wg.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case accountID := <-s.queue:
			ctx, cancel := context.WithTimeout(s.ctx, 50*time.Second)
			if err := s.evaluateAccount(ctx, accountID); err != nil && !errors.Is(err, context.Canceled) {
				slog.Warn("openai_auto_reset_evaluate_failed", "account_id", accountID, "error_code", infraerrors.Reason(err))
			}
			cancel()
			s.pending.Delete(accountID)
		}
	}
}

func (s *OpenAIQuotaAutoResetService) runScanner() {
	defer s.wg.Done()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-s.ctx.Done():
		return
	case <-timer.C:
		s.scanEnabledAccounts(s.ctx)
	}
	ticker := time.NewTicker(openAIAutoResetScanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.scanEnabledAccounts(s.ctx)
		}
	}
}

func (s *OpenAIQuotaAutoResetService) scanEnabledAccounts(ctx context.Context) {
	release, scan := s.tryAcquireScanLock(ctx)
	if !scan {
		return
	}
	if release != nil {
		defer release()
	}
	if repo, ok := s.accountRepo.(openAIQuotaAutomationRepository); ok {
		s.scanQuotaAutomationCandidates(ctx, repo)
		return
	}
	for page := 1; ; page++ {
		accounts, pageInfo, err := s.accountRepo.ListWithFilters(ctx, pagination.PaginationParams{
			Page: page, PageSize: openAIAutoResetBatchSize,
		}, PlatformOpenAI, AccountTypeOAuth, StatusActive, "", 0, "")
		if err != nil {
			slog.Warn("openai_auto_reset_scan_failed", "page", page, "error", err)
			return
		}
		for i := range accounts {
			account := &accounts[i]
			if account.Schedulable && ResolveOpenAIAutoResetCreditConfig(account).Enabled {
				s.Notify(account.ID)
			}
		}
		if len(accounts) < openAIAutoResetBatchSize || pageInfo == nil || page >= pageInfo.Pages {
			return
		}
	}
}

// Redis 锁异常时允许重复扫描，避免协调设施故障导致所有实例同时停止补偿；
// 消费唯一性由数据库幂等记录负责，扫描锁只用于削减重复查询。
func (s *OpenAIQuotaAutoResetService) tryAcquireScanLock(ctx context.Context) (func(), bool) {
	if s.leaderLock == nil {
		return func() {}, true
	}
	ok, err := s.leaderLock.TryAcquireLeaderLock(ctx, openAIAutoResetLeaderLockKey, s.owner, 55*time.Second)
	if err != nil {
		slog.Warn("openai_auto_reset_leader_lock_unavailable", "error", err)
		return func() {}, true
	}
	if !ok {
		return nil, false
	}
	return func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.leaderLock.ReleaseLeaderLock(releaseCtx, openAIAutoResetLeaderLockKey, s.owner)
	}, true
}

type openAIAutoResetAssessment struct {
	triggerWindow string
	resetReached  bool
	pauseReached  bool
	utilization5h float64
	utilization7d float64
	threshold5h   float64
	threshold7d   float64
}

func (s *OpenAIQuotaAutoResetService) evaluateAccount(ctx context.Context, accountID int64) error {
	ctx = withOpenAIAutoResetContext(ctx)
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil || account == nil {
		return err
	}
	if account.IsShadow() {
		if account.ParentAccountID != nil {
			s.Notify(*account.ParentAccountID)
		}
		return nil
	}
	global, err := s.settings.GetOpenAIQuotaAutomationSettings(ctx)
	if err != nil {
		return err
	}
	if isOpenAIQuotaThresholdPaused(account, time.Now()) {
		s.enqueueQuotaPauseNotifications(ctx, account)
	}
	if (isOpenAIQuotaThresholdPaused(account, time.Now()) && (global.AutoRecoverEnabled || global.UsesGlobalReset())) ||
		(global.ExpiryForceResetEnabled && isOpenAIQuotaExpiryResetAccount(account, time.Now())) {
		handled, err := s.evaluateGlobalQuotaAutomation(ctx, account, global)
		if err != nil || handled {
			return err
		}
		account, err = s.accountRepo.GetByID(ctx, accountID)
		if err != nil || account == nil {
			return err
		}
	}
	if global.UsesGlobalReset() {
		return nil
	}
	// 通知补偿会扫描额度暂停账号，但不能让旧用卡流程清除已有临时故障或限流。
	now := time.Now()
	if (account.TempUnschedulableUntil != nil && account.TempUnschedulableUntil.After(now)) ||
		(account.RateLimitResetAt != nil && account.RateLimitResetAt.After(now)) {
		return nil
	}
	config := ResolveOpenAIAutoResetCreditConfig(account)
	if !config.Enabled || !account.IsActive() || !account.Schedulable {
		return nil
	}

	assessment := s.assessExtra(account, config, now)
	state := openAIAutoResetStateFromExtra(account.Extra)
	stateAccount := account
	// 达到用卡阈值本应立即查询以便用卡；但 10 分钟内已确认无卡时，重查不会改变结论，
	// 只会让调度热路径的通知把同一账号的上游额度接口打到十几秒一次。
	needsQuery := openAIAutoResetSnapshotStale(account.Extra, now) ||
		(assessment.resetReached && !openAIAutoResetNoCreditConfirmed(state, now))
	if assessment.pauseReached && !assessment.resetReached {
		needsQuery = needsQuery || state == nil || state.Status == OpenAIAutoResetStatusChecking || state.Status == OpenAIAutoResetStatusFailed || openAIAutoResetStateStale(state, now)
	}
	if needsQuery && openAIAutoResetQueryFailureBackoffActive(state, now) {
		needsQuery = false
	}
	if !needsQuery {
		if !assessment.pauseReached && state != nil && state.TriggerWindow != "" {
			state.TriggerWindow = ""
			state.ErrorCode = ""
			state.CheckedAt = now.UTC().Format(time.RFC3339)
			if state.AvailableCount > 0 {
				state.Status = OpenAIAutoResetStatusAvailable
			} else {
				state.Status = OpenAIAutoResetStatusNoCredit
			}
			return s.persistState(ctx, accountID, state, stateAccount)
		}
		return nil
	}

	checking := &OpenAIAutoResetCreditState{
		Status:         OpenAIAutoResetStatusChecking,
		TriggerWindow:  assessment.triggerWindow,
		AvailableCount: stateAvailableCount(state),
		CheckedAt:      now.UTC().Format(time.RFC3339),
	}
	copyOpenAIAutoResetAttempt(checking, state)
	if err := s.persistState(ctx, accountID, checking, stateAccount); err != nil {
		return err
	}

	usage, err := s.quota.QueryUsage(ctx, accountID)
	if err != nil || usage == nil {
		return s.failState(ctx, accountID, checking, "RESET_CREDIT_QUERY_FAILED", err, stateAccount)
	}
	if err := s.persistFreshUsage(ctx, accountID, usage, now); err != nil {
		return s.failState(ctx, accountID, checking, "USAGE_SNAPSHOT_WRITE_FAILED", err, stateAccount)
	}
	if usage.RateLimitResetCredits == nil {
		return s.failState(ctx, accountID, checking, "RESET_CREDIT_DETAILS_UNAVAILABLE", nil, stateAccount)
	}

	// 查询期间管理员可能关闭开关；消费前重新读取账号，确保尚未发出的任务可取消。
	account, err = s.accountRepo.GetByID(ctx, accountID)
	if err != nil || account == nil {
		return err
	}
	config = ResolveOpenAIAutoResetCreditConfig(account)
	if !config.Enabled {
		return nil
	}
	assessment = s.assessUsage(usage, account, config, now)
	available := usage.RateLimitResetCredits.AvailableCount
	if !assessment.resetReached {
		status := OpenAIAutoResetStatusNoCredit
		if available > 0 {
			status = OpenAIAutoResetStatusAvailable
		}
		return s.persistState(ctx, accountID, &OpenAIAutoResetCreditState{
			Status:         status,
			TriggerWindow:  assessment.triggerWindow,
			AvailableCount: available,
			CheckedAt:      now.UTC().Format(time.RFC3339),
		}, stateAccount)
	}
	if available <= 0 {
		return s.persistState(ctx, accountID, &OpenAIAutoResetCreditState{
			Status:         OpenAIAutoResetStatusNoCredit,
			TriggerWindow:  assessment.triggerWindow,
			AvailableCount: 0,
			CheckedAt:      now.UTC().Format(time.RFC3339),
			LastResultAt:   now.UTC().Format(time.RFC3339),
			ErrorCode:      "NO_RESET_CREDIT",
		}, stateAccount)
	}

	return s.consumeCredit(ctx, account, usage, checking, assessment, nil, stateAccount)
}

// consumeCredit 复用同一核销流程，并在消费前复核账号状态和当前全局策略。
func (s *OpenAIQuotaAutoResetService) consumeCredit(ctx context.Context, account *Account, usage *OpenAIQuotaUsage, checking *OpenAIAutoResetCreditState, assessment openAIAutoResetAssessment, global *OpenAIQuotaAutomationSettings, stateAccount *Account) error {
	accountID := account.ID
	observedAccount := account
	available := usage.RateLimitResetCredits.AvailableCount
	state := checking
	now := time.Now()
	var err error
	cycleSeed := openAIAutoResetCycleSeed(usage)
	selectionState := state
	if global != nil {
		cycleSeed = "pause:" + account.TempUnschedulableReason
		if assessment.triggerWindow == openAIQuotaExpiryResetTrigger {
			cycleSeed = "expiry"
			if !hasPendingOpenAIAutoResetAttempt(state) {
				selectionState = nil
			}
		}
	}
	cycleHash := shortOpenAIAutoResetHash(cycleSeed)
	if global != nil && hasPendingOpenAIAutoResetAttempt(state) {
		cycleHash = state.AttemptCycleHash
	}
	candidate, selectErr := selectOpenAIAutoResetCandidate(usage.autoResetCandidates, available, selectionState, cycleHash)
	replayOnly := global != nil && state.AttemptCycleHash == cycleHash && state.AttemptCreditHash != "" &&
		(selectErr != nil || !assessment.resetReached)
	if selectErr != nil && !replayOnly {
		failed := checking
		failed.AvailableCount = available
		failed.TriggerWindow = assessment.triggerWindow
		failed.AttemptCycleHash = cycleHash
		return s.failState(ctx, accountID, failed, infraerrors.Reason(selectErr), selectErr, stateAccount)
	}
	creditHash := shortOpenAIAutoResetHash(candidate.ID)
	if replayOnly {
		creditHash = state.AttemptCreditHash
	}
	if global != nil && !replayOnly && !globalResetCreditEligible(*global, candidate, now) {
		checking.Status = OpenAIAutoResetStatusAvailable
		checking.AvailableCount = available
		return s.persistState(ctx, accountID, checking, stateAccount)
	}
	stableKey := fmt.Sprintf("oarc:%d:%s:%s", accountID, creditHash, cycleHash)
	redeemRequestID := uuid.NewSHA1(uuid.NameSpaceURL, []byte(stableKey)).String()
	resetting := &OpenAIAutoResetCreditState{
		Status:            OpenAIAutoResetStatusResetting,
		TriggerWindow:     assessment.triggerWindow,
		AvailableCount:    available,
		CheckedAt:         now.UTC().Format(time.RFC3339),
		AttemptCycleHash:  cycleHash,
		AttemptCreditHash: creditHash,
	}
	account, err = s.accountRepo.GetByID(ctx, accountID)
	if err != nil || account == nil || !account.IsActive() || !account.Schedulable {
		return err
	}
	currentPolicy, err := s.settings.refreshOpenAIQuotaAutomationSettings(ctx)
	if err != nil {
		return err
	}
	if global == nil {
		if currentPolicy.UsesGlobalReset() || !ResolveOpenAIAutoResetCreditConfig(account).Enabled {
			return nil
		}
	} else if !replayOnly && !s.globalResetStillEligible(ctx, account, observedAccount, usage, currentPolicy, candidate) {
		return nil
	}
	if err := s.persistState(ctx, accountID, resetting, stateAccount); err != nil {
		return err
	}
	result, err := s.idempotency.Execute(ctx, IdempotencyExecuteOptions{
		Scope:          "openai_auto_reset_credit",
		ActorScope:     fmt.Sprintf("account:%d", accountID),
		Method:         http.MethodPost,
		Route:          "/system/openai/reset-credit/auto",
		IdempotencyKey: stableKey,
		Payload: map[string]any{
			"account_id":  accountID,
			"credit_hash": creditHash,
			"cycle_hash":  cycleHash,
		},
		TTL:        openAIAutoResetAttemptTTL,
		RequireKey: true,
	}, func(execCtx context.Context) (any, error) {
		if replayOnly {
			return nil, infraerrors.Conflict("OPENAI_AUTO_RESET_RESULT_UNCONFIRMED", "the previous reset result cannot be confirmed; refusing another consumption")
		}
		if global != nil {
			fresh, readErr := s.accountRepo.GetByID(execCtx, accountID)
			if readErr != nil {
				return nil, readErr
			}
			latestPolicy, policyErr := s.settings.refreshOpenAIQuotaAutomationSettings(execCtx)
			if policyErr != nil {
				return nil, policyErr
			}
			if !s.globalResetStillEligible(execCtx, fresh, observedAccount, usage, latestPolicy, candidate) {
				return nil, infraerrors.Conflict("OPENAI_QUOTA_AUTOMATION_CHANGED", "account quota pause changed before reset")
			}
		}
		resetResult, resetErr := s.quota.ResetCreditTargeted(execCtx, accountID, candidate.ID, redeemRequestID)
		if resetErr != nil {
			return nil, resetErr
		}
		if resetResult == nil {
			return nil, infraerrors.InternalServer("OPENAI_AUTO_RESET_EMPTY_RESULT", "automatic reset returned an empty result")
		}
		// 幂等表只保存脱敏结果，避免上游返回的卡 ID 被持久化到响应体列。
		return openAIAutoResetConsumeResult{Code: resetResult.Code, WindowsReset: resetResult.WindowsReset}, nil
	})
	if err != nil {
		// 另一个实例已持有同一周期的兑换时保持 resetting，等待下一轮读取同一
		// 幂等结果；不能把并发冲突误报成上游消费失败，更不能改选下一张卡。
		reason := infraerrors.Reason(err)
		if reason == infraerrors.Reason(ErrIdempotencyInProgress) || reason == infraerrors.Reason(ErrIdempotencyRetryBackoff) {
			return nil
		}
		s.recordAudit(accountID, assessment, available, "failed", 0, infraerrors.Reason(err))
		return s.failState(ctx, accountID, resetting, infraerrors.Reason(err), err, stateAccount)
	}

	consumeResult := decodeOpenAIAutoResetConsumeResult(result.Data)
	if strings.EqualFold(strings.TrimSpace(consumeResult.Code), "no_credit") {
		noCreditAt := time.Now().UTC().Format(time.RFC3339)
		noCredit := &OpenAIAutoResetCreditState{
			Status:            OpenAIAutoResetStatusNoCredit,
			TriggerWindow:     assessment.triggerWindow,
			AvailableCount:    0,
			CheckedAt:         noCreditAt,
			LastResultAt:      noCreditAt,
			ErrorCode:         "NO_RESET_CREDIT",
			AttemptCycleHash:  cycleHash,
			AttemptCreditHash: creditHash,
		}
		s.recordAudit(accountID, assessment, available, "no_credit", 0, noCredit.ErrorCode)
		return s.persistState(ctx, accountID, noCredit, stateAccount)
	}
	postCtx, cancelPost := context.WithTimeout(context.WithoutCancel(ctx), 8*time.Second)
	var post OpenAIQuotaResetPostProcessResult
	if global != nil {
		post = s.finishGlobalQuotaReset(postCtx, observedAccount, assessment.triggerWindow == openAIQuotaExpiryResetTrigger)
	} else {
		post = RunOpenAIQuotaResetPostProcess(postCtx, accountID, s.quota, s.recoverer, s.accountRepo.GetByID)
	}
	cancelPost()
	if s.quotaNotifier != nil {
		if consumeResult.Code != "" && consumeResult.Code != "***" {
			s.quotaNotifier.xtuisPush.NotifyAutoResetCreditUsed(account, resetting, candidate.ExpiresAt, consumeResult.WindowsReset, post)
		} else {
			slog.Warn("openai_auto_reset_notification_result_unconfirmed", "account_id", accountID)
		}
	}
	if !post.AccountStateRecovered || post.WarningCode != "" {
		code := post.WarningCode
		if code == "" {
			code = OpenAIQuotaResetWarningAccountRecoveryFailed
		}
		s.recordAudit(accountID, assessment, available, "recovery_failed", consumeResult.WindowsReset, code)
		return s.failState(ctx, accountID, resetting, code, nil, stateAccount)
	}

	successAt := time.Now().UTC().Format(time.RFC3339)
	success := &OpenAIAutoResetCreditState{
		Status:            OpenAIAutoResetStatusSuccess,
		TriggerWindow:     assessment.triggerWindow,
		AvailableCount:    max(0, available-1),
		CheckedAt:         successAt,
		LastResultAt:      successAt,
		AttemptCycleHash:  cycleHash,
		AttemptCreditHash: creditHash,
	}
	if post.Quota != nil && post.Quota.RateLimitResetCredits != nil {
		success.AvailableCount = post.Quota.RateLimitResetCredits.AvailableCount
	}
	if err := s.persistState(ctx, accountID, success, stateAccount); err != nil {
		return err
	}
	s.recordAudit(accountID, assessment, available, "success", consumeResult.WindowsReset, "")
	slog.Info("openai_auto_reset_credit_success",
		"account_id", accountID,
		"trigger_window", assessment.triggerWindow,
		"threshold_5h", assessment.threshold5h,
		"threshold_7d", assessment.threshold7d,
		"utilization_5h", assessment.utilization5h,
		"utilization_7d", assessment.utilization7d,
		"windows_reset", consumeResult.WindowsReset,
	)
	return nil
}

type openAIAutoResetConsumeResult struct {
	Code         string `json:"result_code"`   // 核销业务状态，避免被通用 code 凭据脱敏规则覆盖。
	WindowsReset int    `json:"windows_reset"` // 上游确认重置的额度窗口数量。
}

func decodeOpenAIAutoResetConsumeResult(value any) openAIAutoResetConsumeResult {
	if typed, ok := value.(openAIAutoResetConsumeResult); ok {
		return typed
	}
	raw, _ := json.Marshal(value)
	var decoded struct {
		openAIAutoResetConsumeResult        // 当前格式的脱敏核销结果。
		LegacyCode                   string `json:"code"` // 兼容旧幂等记录的业务状态字段。
	}
	_ = json.Unmarshal(raw, &decoded)
	if decoded.Code == "" {
		decoded.Code = decoded.LegacyCode
	}
	return decoded.openAIAutoResetConsumeResult
}

func (s *OpenAIQuotaAutoResetService) assessExtra(account *Account, config OpenAIAutoResetCreditConfig, now time.Time) openAIAutoResetAssessment {
	utilization5h, _ := resolveOpenAIQuotaUtilization(account.Extra, "5h", now)
	utilization7d, _ := resolveOpenAIQuotaUtilization(account.Extra, "7d", now)
	return s.buildAssessment(account, config, utilization5h, utilization7d)
}

func (s *OpenAIQuotaAutoResetService) assessUsage(usage *OpenAIQuotaUsage, account *Account, config OpenAIAutoResetCreditConfig, now time.Time) openAIAutoResetAssessment {
	updates := buildOpenAIAutoResetUsageUpdates(usage, now)
	utilization5h := readOpenAIQuotaUsedPercent(updates, "5h") / 100
	utilization7d := readOpenAIQuotaUsedPercent(updates, "7d") / 100
	return s.buildAssessment(account, config, utilization5h, utilization7d)
}

func (s *OpenAIQuotaAutoResetService) buildAssessment(account *Account, config OpenAIAutoResetCreditConfig, utilization5h, utilization7d float64) openAIAutoResetAssessment {
	assessment := openAIAutoResetAssessment{
		utilization5h: utilization5h,
		utilization7d: utilization7d,
		threshold5h:   config.Threshold5h,
		threshold7d:   config.Threshold7d,
	}
	reset5h := utilization5h >= config.Threshold5h
	reset7d := utilization7d >= config.Threshold7d
	assessment.resetReached = reset5h || reset7d
	assessment.triggerWindow = joinOpenAIAutoResetWindows(reset5h, reset7d)

	pause5h, pause7d := resolveOpenAIQuotaAutoPauseThresholds(context.Background(), account)
	if s.settings != nil {
		pause5h, pause7d = resolveOpenAIQuotaAutoPauseThresholds(
			withOpenAIQuotaAutoPauseSettings(context.Background(), s.settings.GetOpenAIQuotaAutoPauseSettings(context.Background())),
			account,
		)
	}
	pauseReached5h := !resolveAccountExtraBool(account.Extra, "auto_pause_5h_disabled") && pause5h > 0 && utilization5h >= pause5h
	pauseReached7d := !resolveAccountExtraBool(account.Extra, "auto_pause_7d_disabled") && pause7d > 0 && utilization7d >= pause7d
	assessment.pauseReached = pauseReached5h || pauseReached7d || assessment.resetReached
	if assessment.triggerWindow == "" {
		assessment.triggerWindow = joinOpenAIAutoResetWindows(pauseReached5h, pauseReached7d)
	}
	return assessment
}

func joinOpenAIAutoResetWindows(fiveHour, sevenDay bool) string {
	switch {
	case fiveHour && sevenDay:
		return "5h+7d"
	case fiveHour:
		return "5h"
	case sevenDay:
		return "7d"
	default:
		return ""
	}
}

func buildOpenAIAutoResetUsageUpdates(usage *OpenAIQuotaUsage, now time.Time) map[string]any {
	if usage == nil || usage.RateLimit == nil {
		return nil
	}
	rateLimit := usage.RateLimit
	snapshot := &OpenAICodexUsageSnapshot{UpdatedAt: now.UTC().Format(time.RFC3339)}
	applyWindow := func(window *OpenAIRateLimitWindow, primary bool) {
		if window == nil {
			return
		}
		used := window.UsedPercent
		resetAfter := int(window.ResetAfterSeconds)
		windowMinutes := int(window.LimitWindowSeconds / 60)
		if primary {
			snapshot.PrimaryUsedPercent = &used
			snapshot.PrimaryResetAfterSeconds = &resetAfter
			snapshot.PrimaryWindowMinutes = &windowMinutes
		} else {
			snapshot.SecondaryUsedPercent = &used
			snapshot.SecondaryResetAfterSeconds = &resetAfter
			snapshot.SecondaryWindowMinutes = &windowMinutes
		}
	}
	applyWindow(rateLimit.PrimaryWindow, true)
	applyWindow(rateLimit.SecondaryWindow, false)
	return buildCodexUsageExtraUpdates(snapshot, now)
}

func (s *OpenAIQuotaAutoResetService) persistFreshUsage(ctx context.Context, accountID int64, usage *OpenAIQuotaUsage, now time.Time) error {
	updates := buildOpenAIAutoResetUsageUpdates(usage, now)
	if len(updates) > 0 {
		if err := s.accountRepo.UpdateExtra(ctx, accountID, updates); err != nil {
			return err
		}
	}
	return s.quota.CacheResetCreditsSnapshot(ctx, accountID, usage.RateLimitResetCredits)
}

func selectOpenAIAutoResetCandidate(candidates []openAIAutoResetCreditCandidate, available int, previous *OpenAIAutoResetCreditState, cycleHash string) (openAIAutoResetCreditCandidate, error) {
	if available <= 0 {
		return openAIAutoResetCreditCandidate{}, infraerrors.Conflict("OPENAI_AUTO_RESET_NO_CREDIT", "no reset credit is available")
	}
	if len(candidates) < available {
		return openAIAutoResetCreditCandidate{}, infraerrors.Conflict("OPENAI_AUTO_RESET_CREDIT_DETAILS_INCOMPLETE", "reset credit details are incomplete")
	}
	for _, candidate := range candidates {
		if _, err := time.Parse(time.RFC3339, candidate.ExpiresAt); err != nil {
			return openAIAutoResetCreditCandidate{}, infraerrors.Conflict("OPENAI_AUTO_RESET_CREDIT_EXPIRY_INVALID", "reset credit expiration is invalid")
		}
	}
	sorted := append([]openAIAutoResetCreditCandidate(nil), candidates...)
	sort.SliceStable(sorted, func(i, j int) bool {
		left, leftErr := time.Parse(time.RFC3339, sorted[i].ExpiresAt)
		right, rightErr := time.Parse(time.RFC3339, sorted[j].ExpiresAt)
		if leftErr != nil {
			return false
		}
		if rightErr != nil {
			return true
		}
		return left.Before(right)
	})
	if previous != nil && previous.AttemptCycleHash == cycleHash && previous.AttemptCreditHash != "" {
		for _, candidate := range sorted {
			if shortOpenAIAutoResetHash(candidate.ID) == previous.AttemptCreditHash {
				if strings.TrimSpace(candidate.ID) == "" {
					break
				}
				return candidate, nil
			}
		}
		return openAIAutoResetCreditCandidate{}, infraerrors.Conflict("OPENAI_AUTO_RESET_ORIGINAL_CREDIT_UNAVAILABLE", "the original reset credit cannot be confirmed; refusing to switch credits")
	}
	if len(sorted) == 0 || strings.TrimSpace(sorted[0].ID) == "" {
		return openAIAutoResetCreditCandidate{}, infraerrors.Conflict("OPENAI_AUTO_RESET_CREDIT_ID_MISSING", "the earliest reset credit has no official id")
	}
	return sorted[0], nil
}

func openAIAutoResetCycleSeed(usage *OpenAIQuotaUsage) string {
	if usage == nil || usage.RateLimit == nil {
		return "5h:0|7d:0"
	}
	var fiveHour, sevenDay int64
	for _, window := range []*OpenAIRateLimitWindow{usage.RateLimit.PrimaryWindow, usage.RateLimit.SecondaryWindow} {
		if window == nil {
			continue
		}
		resetAt := window.ResetAt
		if resetAt <= 0 {
			resetAt = usage.FetchedAt + window.ResetAfterSeconds
		}
		if window.LimitWindowSeconds <= 6*60*60 {
			fiveHour = resetAt
		} else {
			sevenDay = resetAt
		}
	}
	return fmt.Sprintf("5h:%d|7d:%d", fiveHour, sevenDay)
}

func shortOpenAIAutoResetHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:12])
}

func openAIAutoResetSnapshotStale(extra map[string]any, now time.Time) bool {
	if len(extra) == 0 {
		return true
	}
	raw, ok := extra["codex_usage_updated_at"]
	if !ok {
		return true
	}
	updatedAt, err := parseTime(fmt.Sprint(raw))
	return err != nil || now.Sub(updatedAt) >= openAIAutoResetSnapshotTTL
}

func openAIAutoResetStateFromExtra(extra map[string]any) *OpenAIAutoResetCreditState {
	if len(extra) == 0 {
		return nil
	}
	raw, ok := extra[OpenAIAutoResetCreditStateExtraKey]
	if !ok || raw == nil {
		return nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var state OpenAIAutoResetCreditState
	if err := json.Unmarshal(encoded, &state); err != nil || state.Status == "" {
		return nil
	}
	return &state
}

// openAIAutoResetNoCreditConfirmed 报告账号在状态有效期内已被确认无卡。
func openAIAutoResetNoCreditConfirmed(state *OpenAIAutoResetCreditState, now time.Time) bool {
	return state != nil && state.Status == OpenAIAutoResetStatusNoCredit && !openAIAutoResetStateStale(state, now)
}

// openAIAutoResetQueryFailureBackoffActive 报告查询阶段刚失败、仍在重试间隔内。
// 只覆盖查询、快照写入、卡明细缺失三类失败；用卡阶段的失败需要立即按原幂等键重试。
func openAIAutoResetQueryFailureBackoffActive(state *OpenAIAutoResetCreditState, now time.Time) bool {
	if state == nil || state.Status != OpenAIAutoResetStatusFailed {
		return false
	}
	switch state.ErrorCode {
	case "RESET_CREDIT_QUERY_FAILED", "USAGE_SNAPSHOT_WRITE_FAILED", "RESET_CREDIT_DETAILS_UNAVAILABLE":
	default:
		return false
	}
	failedAt, err := time.Parse(time.RFC3339, state.LastResultAt)
	if err != nil {
		return false
	}
	elapsed := now.Sub(failedAt)
	return elapsed >= 0 && elapsed < openAIAutoResetQueryFailureRetryAfter
}

func openAIAutoResetStateStale(state *OpenAIAutoResetCreditState, now time.Time) bool {
	if state == nil || state.CheckedAt == "" {
		return true
	}
	checkedAt, err := time.Parse(time.RFC3339, state.CheckedAt)
	return err != nil || now.Sub(checkedAt) >= openAIAutoResetSnapshotTTL
}

func stateAvailableCount(state *OpenAIAutoResetCreditState) int {
	if state == nil {
		return 0
	}
	return state.AvailableCount
}

func copyOpenAIAutoResetAttempt(target, source *OpenAIAutoResetCreditState) {
	if target == nil || source == nil {
		return
	}
	target.AttemptCycleHash = source.AttemptCycleHash
	target.AttemptCreditHash = source.AttemptCreditHash
}

// persistState 通过条件更新保护核销状态，拒绝迟到任务覆盖较新的卡片尝试。
func (s *OpenAIQuotaAutoResetService) persistState(ctx context.Context, accountID int64, state *OpenAIAutoResetCreditState, observed *Account) error {
	if state == nil {
		return nil
	}
	if repo, ok := s.accountRepo.(interface {
		CompareAndSwapOpenAIAutoResetState(context.Context, int64, any, *OpenAIAutoResetCreditState) (bool, error)
	}); ok {
		changed, err := repo.CompareAndSwapOpenAIAutoResetState(ctx, accountID, observed.Extra[OpenAIAutoResetCreditStateExtraKey], state)
		if err != nil {
			return err
		}
		if !changed {
			return infraerrors.Conflict("OPENAI_AUTO_RESET_STATE_CHANGED", "automatic reset state changed before update")
		}
		if observed.Extra == nil {
			observed.Extra = make(map[string]any)
		}
		next := *state
		observed.Extra[OpenAIAutoResetCreditStateExtraKey] = &next
		return nil
	}
	return s.accountRepo.UpdateExtra(ctx, accountID, map[string]any{OpenAIAutoResetCreditStateExtraKey: state})
}

// failState 保存本次失败结果，并保留未确认卡片的重放标识。
func (s *OpenAIQuotaAutoResetService) failState(ctx context.Context, accountID int64, state *OpenAIAutoResetCreditState, code string, cause error, observed *Account) error {
	if state == nil {
		state = &OpenAIAutoResetCreditState{}
	}
	if strings.TrimSpace(code) == "" {
		code = "OPENAI_AUTO_RESET_FAILED"
	}
	state.Status = OpenAIAutoResetStatusFailed
	state.ErrorCode = code
	state.LastResultAt = time.Now().UTC().Format(time.RFC3339)
	if err := s.persistState(ctx, accountID, state, observed); err != nil {
		return err
	}
	slog.Warn("openai_auto_reset_credit_failed",
		"account_id", accountID,
		"trigger_window", state.TriggerWindow,
		"available_count", state.AvailableCount,
		"error_code", code,
	)
	if cause != nil {
		return cause
	}
	return infraerrors.Conflict(code, "automatic reset credit operation failed")
}

func (s *OpenAIQuotaAutoResetService) recordAudit(accountID int64, assessment openAIAutoResetAssessment, available int, resultCode string, windowsReset int, errorCode string) {
	if s.audit == nil {
		return
	}
	statusCode := http.StatusOK
	if resultCode != "success" {
		statusCode = http.StatusConflict
	}
	s.audit.Record(&AuditLog{
		ActorEmail: "system",
		ActorRole:  "system",
		AuthMethod: "system",
		Action:     "system.openai.reset_credit.auto",
		Method:     "SYSTEM",
		Path:       fmt.Sprintf("/system/openai/accounts/%d/auto-reset-credit", accountID),
		StatusCode: statusCode,
		Extra: map[string]any{
			"account_id":      accountID,
			"trigger_window":  assessment.triggerWindow,
			"threshold_5h":    assessment.threshold5h,
			"threshold_7d":    assessment.threshold7d,
			"utilization_5h":  assessment.utilization5h,
			"utilization_7d":  assessment.utilization7d,
			"available_count": available,
			"result_code":     resultCode,
			"windows_reset":   windowsReset,
			"error_code":      errorCode,
		},
	})
}

var openAIAutoResetNotifierRegistry struct {
	sync.RWMutex
	service *OpenAIQuotaAutoResetService
}

func setOpenAIAutoResetNotifier(service *OpenAIQuotaAutoResetService) {
	openAIAutoResetNotifierRegistry.Lock()
	openAIAutoResetNotifierRegistry.service = service
	openAIAutoResetNotifierRegistry.Unlock()
}

func clearOpenAIAutoResetNotifier(service *OpenAIQuotaAutoResetService) {
	openAIAutoResetNotifierRegistry.Lock()
	if openAIAutoResetNotifierRegistry.service == service {
		openAIAutoResetNotifierRegistry.service = nil
	}
	openAIAutoResetNotifierRegistry.Unlock()
}

func notifyOpenAIAutoReset(accountID int64) {
	openAIAutoResetNotifierRegistry.RLock()
	service := openAIAutoResetNotifierRegistry.service
	openAIAutoResetNotifierRegistry.RUnlock()
	if service != nil {
		service.Notify(accountID)
	}
}

// NotifyOpenAIAutoResetCredit 供额度查询入口发送轻量信号；不执行同步上游请求。
func NotifyOpenAIAutoResetCredit(accountID int64) {
	notifyOpenAIAutoReset(accountID)
}

// openAIAutoResetSchedulerNotifiedAt 记录调度热路径最近一次为某账号发出通知的时间。
var openAIAutoResetSchedulerNotifiedAt sync.Map // accountID(int64) -> time.Time

// notifyOpenAIAutoResetFromScheduler 供调度候选过滤使用。候选过滤按请求逐账号执行，
// 每条通知都会让后台读一次账号，不做冷却时无卡或待用卡的账号会持续占满后台协程。
func notifyOpenAIAutoResetFromScheduler(accountID int64) {
	notifyOpenAIAutoResetFromSchedulerAt(accountID, time.Now())
}

func notifyOpenAIAutoResetFromSchedulerAt(accountID int64, now time.Time) bool {
	if accountID <= 0 {
		return false
	}
	if last, ok := openAIAutoResetSchedulerNotifiedAt.Load(accountID); ok {
		if lastAt, ok := last.(time.Time); ok {
			elapsed := now.Sub(lastAt)
			if elapsed >= 0 && elapsed < openAIAutoResetSchedulerNotifyCooldown {
				return false
			}
		}
	}
	openAIAutoResetSchedulerNotifiedAt.Store(accountID, now)
	notifyOpenAIAutoReset(accountID)
	return true
}
