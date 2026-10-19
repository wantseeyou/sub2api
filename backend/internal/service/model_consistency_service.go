package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/modeltrace"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

var (
	ErrModelConsistencyUnavailable   = errors.New("model consistency diagnostics unavailable")
	ErrModelConsistencyBusy          = errors.New("model consistency diagnostics busy")
	ErrModelConsistencyUnsupported   = errors.New("unsupported model consistency account")
	ErrModelConsistencyModelsInvalid = errors.New("select between 1 and 8 distinct models")
)

type modelConsistencyWriter interface {
	SaveModelConsistencyResults(context.Context, *Account, map[string]ModelConsistencySnapshot, bool) (bool, error)
	CancelModelConsistencyRun(context.Context, int64, string) (bool, error)
}

// ModelConsistencyCache 提供跨实例的有界租约与运行取消通知。
type ModelConsistencyCache interface {
	Acquire(context.Context, int64, string, time.Time) (bool, error)
	Release(context.Context, int64, string) error
	SubscribeCancel(context.Context, int64, string) (ModelConsistencyCancelSubscription, error)
	PublishCancel(context.Context, int64, string) error
}

// ModelConsistencyCancelSubscription 屏蔽取消通知的具体消息传输实现。
type ModelConsistencyCancelSubscription interface {
	Wait(context.Context) error
	Close() error
}

// ModelConsistencyService 隔离执行管理员诊断，不触发业务调度或用户计费。
type ModelConsistencyService struct {
	accounts    AccountRepository     // 账号读取与条件保存入口。
	proxies     ProxyRepository       // 指定代理配置读取入口。
	gateway     *OpenAIGatewayService // 仅复用协议构造与底层传输能力。
	concurrency *ConcurrencyService   // 与业务共用的账号并发限制。
	cache       ModelConsistencyCache // 跨实例租约和取消通知。
	ctx         context.Context       // 服务生命周期上下文。
	cancel      context.CancelFunc    // 停止服务时取消全部诊断。
	mu          sync.Mutex            // 保护关闭状态与任务登记。
	wg          sync.WaitGroup        // 等待有界运行任务退出。
	stopped     bool                  // 服务是否已停止接收诊断。
}

// NewModelConsistencyService 创建无自动探测的独立诊断服务。
func NewModelConsistencyService(accounts AccountRepository, proxies ProxyRepository, gateway *OpenAIGatewayService, concurrency *ConcurrencyService, cache ModelConsistencyCache) *ModelConsistencyService {
	ctx, cancel := context.WithCancel(context.Background())
	return &ModelConsistencyService{accounts: accounts, proxies: proxies, gateway: gateway, concurrency: concurrency, cache: cache, ctx: ctx, cancel: cancel}
}

// Stop 取消正在执行的诊断并等待其关闭专用连接。
func (s *ModelConsistencyService) Stop() {
	s.mu.Lock()
	s.stopped = true
	s.cancel()
	s.mu.Unlock()
	s.wg.Wait()
}

// modelConsistencyContext 创建不会携带业务身份和状态的请求上下文。
func modelConsistencyContext(ctx context.Context) *gin.Context {
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, "/v1/responses", nil)
	c := &gin.Context{Request: request}
	c.Set("codex_diagnostic_request", true)
	SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
	return c
}

// loadAccount 加载指定代理；代理解析失败时保留失败原因而不改成直连。
func (s *ModelConsistencyService) loadAccount(ctx context.Context, id int64) (*Account, string, error) {
	account, err := s.accounts.GetByID(ctx, id)
	if err != nil {
		return nil, "", err
	}
	copy := *account
	account = &copy
	if account.IsShadow() {
		parent, err := resolveCredentialAccount(ctx, s.accounts, account)
		if err != nil {
			return account, "credentials_unavailable", nil
		}
		account.Credentials = parent.Credentials
	}
	if account.ProxyID != nil {
		proxy, err := s.proxies.GetByID(ctx, *account.ProxyID)
		if err != nil || proxy == nil || !proxy.IsActive() || proxy.IsExpired(time.Now()) {
			account.Proxy = nil
			return account, "proxy_unavailable", nil
		}
		account.Proxy = proxy
	}
	return account, "", nil
}

// configuration 复用 HTTP 入站的传输选择，并计算插件和配置摘要。
func (s *ModelConsistencyService) configuration(account *Account) (OpenAIWSProtocolDecision, string, error) {
	decision := s.gateway.resolveOpenAIForwardTransport(modelConsistencyContext(s.ctx), account)
	var plugin any
	if manager := s.gateway.pluginManager; manager != nil && manager.ShouldRouteOpenAIOAuth(account) {
		if route := manager.route.Load(); route != nil {
			plugin = []any{route.pluginID, route.rolloutPercent, route.unavailable}
			if route.runtime != nil {
				plugin = []any{plugin, route.runtime.installation}
			}
		}
	}
	global := []any{CodexCanonicalUserAgent(), CodexCanonicalClientVersion()}
	if cfg := s.gateway.cfg; cfg != nil {
		global = append(global, cfg.Gateway.ForceCodexCLI, cfg.Gateway.OpenAIWS)
	}
	digest, err := modelConsistencyConfigDigest(account, decision, []any{plugin, global})
	return decision, digest, err
}

// Latest 按当前配置和截止时间展示最近结果，不把遗留运行态显示为仍在执行。
func (s *ModelConsistencyService) Latest(ctx context.Context, id int64) (*ModelConsistencyView, error) {
	account, _, err := s.loadAccount(ctx, id)
	if err != nil {
		return nil, err
	}
	models, err := modeltrace.ModelTraceGPTModels()
	if err != nil {
		return nil, ErrModelConsistencyUnavailable
	}
	view := &ModelConsistencyView{Supported: isModelConsistencyAccount(account), ReferenceModels: models, Results: []ModelConsistencyResult{}}
	if !view.Supported {
		view.UnsupportedReason = "unsupported_account"
	}
	_, digest, err := s.configuration(account)
	if err != nil {
		return nil, ErrModelConsistencyUnavailable
	}
	results, err := modelConsistencySnapshots(account)
	if err != nil {
		return nil, ErrModelConsistencyUnavailable
	}
	for model, result := range results {
		result.Applicable = view.Supported && result.ConfigDigest == digest
		if !result.Applicable {
			result.Reason = "configuration_changed"
			if result.Status == "running" {
				result.Status = "inconclusive"
			}
		}
		if result.Status == "running" && !result.DeadlineAt.After(time.Now()) {
			result.Status = "expired"
			result.Reason = "timeout"
		}
		results[model] = result
	}
	view.Results = modelConsistencyOrderedResults(results)
	return view, nil
}

// Start 校验模型并取得跨实例租约后，登记快照并启动有截止时间的串行诊断。
func (s *ModelConsistencyService) Start(ctx context.Context, id int64, models []string) (*ModelConsistencyRun, error) {
	if len(models) == 0 || len(models) > modelConsistencyMaxModels {
		return nil, ErrModelConsistencyModelsInvalid
	}
	seen := make(map[string]bool, len(models))
	for i, model := range models {
		model = strings.TrimSpace(model)
		if len(model) == 0 || len(model) > 128 || seen[model] || strings.ContainsAny(model, "\x00\r\n") {
			return nil, ErrModelConsistencyModelsInvalid
		}
		models[i] = model
		seen[model] = true
	}
	writer, ok := s.accounts.(modelConsistencyWriter)
	if !ok || s.cache == nil || s.concurrency == nil {
		return nil, ErrModelConsistencyUnavailable
	}
	account, preflight, err := s.loadAccount(ctx, id)
	if err != nil {
		return nil, err
	}
	if !isModelConsistencyAccount(account) {
		return nil, ErrModelConsistencyUnsupported
	}
	decision, digest, err := s.configuration(account)
	if err != nil {
		return nil, ErrModelConsistencyUnavailable
	}
	references, err := modeltrace.ModelTraceGPTModels()
	if err != nil {
		return nil, ErrModelConsistencyUnavailable
	}
	known := make(map[string]bool, len(references))
	for _, model := range references {
		known[model] = true
	}
	runID, now := uuid.NewString(), time.Now().UTC()
	deadline := now.Add(modelConsistencyDeadline)
	release, err := s.acquire(ctx, id, runID, deadline)
	if err != nil {
		return nil, err
	}
	owned := false
	defer func() {
		if !owned {
			release()
		}
	}()
	results := make(map[string]ModelConsistencySnapshot, len(models))
	runnable := false
	for _, model := range models {
		_, upstream := resolveOpenAIForwardMappedModels(account, model, false)
		result := ModelConsistencySnapshot{ModelConsistencyResult: ModelConsistencyResult{
			RunID: runID, RequestedModel: model, UpstreamModel: upstream, Status: "running", StartedAt: now, DeadlineAt: deadline, Applicable: true,
			Transport: "http", TransportReason: decision.Reason, Profile: modelConsistencyProfile,
			Versions:   ModelConsistencyVersions{Bank: modeltrace.ModelTraceBankCommit(), Algorithm: modeltrace.AlgorithmVersion, Challenge: modeltrace.ChallengeVersion, Profile: modelConsistencyProfile},
			Limitation: "reference_accuracy_uncalibrated",
		}, ConfigDigest: digest}
		if decision.Transport == OpenAIUpstreamTransportResponsesWebsocketV2 {
			result.Transport = "websocket"
		}
		switch {
		case !known[upstream]:
			result.Status, result.Reason = "inconclusive", "fingerprint_missing"
		case preflight != "":
			result.Status, result.Reason = "failed", preflight
		case decision.Transport == OpenAIUpstreamTransportResponsesWebsocket:
			result.Status, result.Reason = "inconclusive", "unsupported_transport"
		default:
			runnable = true
		}
		if result.Status != "running" {
			completed := now
			result.CompletedAt = &completed
		}
		results[model] = result
	}
	var subscription ModelConsistencyCancelSubscription
	if runnable {
		subscription, err = s.cache.SubscribeCancel(ctx, id, runID)
		if err != nil {
			return nil, ErrModelConsistencyUnavailable
		}
	}
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		if subscription != nil {
			_ = subscription.Close()
		}
		return nil, ErrModelConsistencyUnavailable
	}
	// 与 Stop 的 Wait 同步登记，避免启动过程中服务退出。
	s.wg.Add(1)
	s.mu.Unlock()
	started, err := writer.SaveModelConsistencyResults(ctx, account, results, false)
	if err != nil || !started {
		s.wg.Done()
		if subscription != nil {
			_ = subscription.Close()
		}
		return nil, ErrModelConsistencyUnavailable
	}
	response := &ModelConsistencyRun{RunID: runID, Results: modelConsistencyOrderedResults(results)}
	if !runnable {
		s.wg.Done()
		return response, nil
	}
	owned = true
	go s.run(account, models, results, subscription, release, deadline)
	return response, nil
}

// Cancel 使用共享快照和消息通知取消指定运行，避免取消后迟到结果覆盖状态。
func (s *ModelConsistencyService) Cancel(ctx context.Context, id int64, runID string) (bool, error) {
	if _, err := uuid.Parse(runID); err != nil {
		return false, ErrModelConsistencyModelsInvalid
	}
	writer, ok := s.accounts.(modelConsistencyWriter)
	if !ok || s.cache == nil {
		return false, ErrModelConsistencyUnavailable
	}
	changed, err := writer.CancelModelConsistencyRun(ctx, id, runID)
	if err != nil {
		return false, err
	}
	if err := s.cache.PublishCancel(ctx, id, runID); err != nil {
		return changed, ErrModelConsistencyUnavailable
	}
	return changed, nil
}

// acquire 在同一原子操作中限制全局四个诊断和同账号单一运行。
func (s *ModelConsistencyService) acquire(ctx context.Context, id int64, runID string, deadline time.Time) (func(), error) {
	acquired, err := s.cache.Acquire(ctx, id, runID, deadline)
	if err != nil {
		return nil, ErrModelConsistencyUnavailable
	}
	if !acquired {
		return nil, ErrModelConsistencyBusy
	}
	return func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := s.cache.Release(releaseCtx, id, runID); err != nil {
			slog.Warn("model_consistency_lease_release_failed", "account_id", id, "run_id", runID)
		}
	}, nil
}

// run 串行执行有限模型与样本，并在退出时关闭取消监听和共享租约。
func (s *ModelConsistencyService) run(account *Account, models []string, results map[string]ModelConsistencySnapshot, subscription ModelConsistencyCancelSubscription, release func(), deadline time.Time) {
	defer s.wg.Done()
	defer release()
	ctx, cancel := context.WithDeadline(s.ctx, deadline)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = subscription.Wait(ctx)
		cancel()
	}()
	defer func() { _ = subscription.Close(); <-done }()
	for _, model := range models {
		result := results[model]
		if result.Status != "running" {
			continue
		}
		if ctx.Err() == nil {
			s.evaluate(ctx, account, &result)
		}
		if ctx.Err() != nil {
			result.Status, result.Reason = "cancelled", "cancelled"
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				result.Status, result.Reason = "expired", "timeout"
			}
		}
		completed := time.Now().UTC()
		result.CompletedAt = &completed
		results[model] = result
		// 每个模型独立落库，保留后续采样超时或取消前已经完成的结果。
		s.saveResult(account.ID, result)
	}
}

// saveResult 在截止前保存已完成模型，沿用运行标识、配置及版本条件防止迟到覆盖。
func (s *ModelConsistencyService) saveResult(accountID int64, result ModelConsistencySnapshot) {
	// 截止后的快照由读取端显示 expired，不再接受迟到执行写入。
	if !time.Now().Before(result.DeadlineAt) {
		return
	}
	persistCtx, persistCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer persistCancel()
	current, _, err := s.loadAccount(persistCtx, accountID)
	if err != nil {
		slog.Warn("model_consistency_configuration_check_failed", "account_id", accountID)
		return
	}
	_, digest, err := s.configuration(current)
	if err != nil || digest != result.ConfigDigest {
		return
	}
	writer, ok := s.accounts.(modelConsistencyWriter)
	if !ok {
		return
	}
	results := map[string]ModelConsistencySnapshot{result.RequestedModel: result}
	saved, err := writer.SaveModelConsistencyResults(persistCtx, current, results, true)
	if err == nil && !saved {
		// 用量刷新也会推进 updated_at；仅在配置摘要仍一致时重读并重试一次。
		current, _, err = s.loadAccount(persistCtx, accountID)
		if err == nil {
			_, digest, err = s.configuration(current)
			if err == nil && digest == result.ConfigDigest {
				saved, err = writer.SaveModelConsistencyResults(persistCtx, current, results, true)
			}
		}
	}
	if err != nil {
		slog.Warn("model_consistency_result_save_failed", "account_id", accountID)
	} else if !saved {
		slog.Info("model_consistency_result_save_rejected", "account_id", accountID)
	}
}

// evaluate 仅在三个完整有效的独立样本候选一致时给出经验线索。
func (s *ModelConsistencyService) evaluate(ctx context.Context, account *Account, result *ModelConsistencySnapshot) {
	var candidate string
	var score float64
	outputs := make(map[string]bool, 3)
	for sample := 0; sample < 3; sample++ {
		if ctx.Err() != nil {
			return
		}
		current, reason, err := s.loadAccount(ctx, account.ID)
		if err != nil || reason != "" {
			result.Status, result.Reason = "failed", "configuration_changed"
			return
		}
		_, digest, err := s.configuration(current)
		if err != nil || digest != result.ConfigDigest {
			result.Status, result.Reason = "inconclusive", "configuration_changed"
			return
		}
		snapshots, err := modelConsistencySnapshots(current)
		if err != nil {
			result.Status, result.Reason = "failed", "configuration_changed"
			return
		}
		stored := snapshots[result.RequestedModel]
		if stored.RunID != result.RunID || stored.Status != "running" {
			result.Status, result.Reason = "cancelled", "cancelled"
			return
		}
		challenge, err := modeltrace.NewModelTraceChallenge()
		if err != nil {
			result.Status, result.Reason = "failed", "challenge_unavailable"
			return
		}
		result.SampleCount++
		output, usage, reason := s.sample(ctx, account, result.UpstreamModel, challenge, result.Transport)
		if usage != nil {
			if result.Usage == nil {
				result.Usage = &ModelConsistencyUsage{}
			}
			result.Usage.InputTokens += usage.InputTokens
			result.Usage.OutputTokens += usage.OutputTokens
		}
		if reason != "" {
			result.Status, result.Reason = "failed", reason
			return
		}
		prediction, err := modeltrace.ModelTracePredict(output, challenge.ExpectedCount)
		numbers, validityErr := modeltrace.Validate(output, challenge.ExpectedCount)
		normalized := fmt.Sprint(numbers)
		if err != nil || validityErr != nil || outputs[normalized] {
			result.Status, result.Reason = "inconclusive", "invalid_sample"
			return
		}
		outputs[normalized] = true
		result.ValidSampleCount++
		if candidate != "" && candidate != prediction.Model {
			result.Status, result.Reason = "inconclusive", "conflicting_samples"
			return
		}
		candidate = prediction.Model
		score += prediction.Probability / 3
	}
	result.CandidateModel, result.Score = candidate, &score
	result.Status, result.Reason = "suspected_mismatch", "different_reference"
	if candidate == result.UpstreamModel {
		result.Status, result.Reason = "matched", "matched_reference"
	}
}
