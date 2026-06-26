package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	externalAccountSyncDefaultInterval       = 10 * time.Second
	externalAccountSyncDefaultRequestTimeout = 5 * time.Second

	externalTokenExportManagedKey   = "external_token_export_managed"
	externalTokenExportEmailKey     = "external_token_export_email"
	externalTokenExportSourceIDKey  = "external_token_export_source_id"
	externalTokenExportUpdatedAtKey = "external_token_export_updated_at"
)

// ExternalAccountSyncSettingReader 读取外部账号同步配置。
type ExternalAccountSyncSettingReader interface {
	GetExternalAccountSyncURL(ctx context.Context) string
}

// ExternalAccountSyncAccountRepository 提供外部账号同步需要的账号写入能力。
type ExternalAccountSyncAccountRepository interface {
	Create(ctx context.Context, account *Account) error
	Update(ctx context.Context, account *Account) error
	ListByPlatformTypeCredentialEmail(ctx context.Context, platform, accountType, email string) ([]Account, error)
}

// ExternalAccountSyncOptions 配置外部账号同步服务运行参数。
type ExternalAccountSyncOptions struct {
	Interval           time.Duration // 定时同步间隔。
	RequestTimeout     time.Duration // 单次上游请求超时时间。
	DefaultConcurrency int           // 新建账号默认并发数。
}

// ExternalAccountSyncService 从外部 token-export 接口同步账号。
type ExternalAccountSyncService struct {
	settings           ExternalAccountSyncSettingReader     // 系统设置读取器。
	accountRepo        ExternalAccountSyncAccountRepository // 账号仓储。
	client             *http.Client                         // 上游 HTTP 客户端。
	interval           time.Duration                        // 定时同步间隔。
	requestTimeout     time.Duration                        // 单次同步超时时间。
	defaultConcurrency int                                  // 新建账号默认并发数。
	triggerCh          chan string                          // 立即同步触发队列。
	stopCh             chan struct{}                        // 停止信号。
	stopOnce           sync.Once                            // 停止流程幂等控制。
	wg                 sync.WaitGroup                       // 后台协程等待组。
	running            int32                                // 当前是否已有同步运行。
}

type externalAccountSyncResponse struct {
	Items []externalAccountSyncItem `json:"items"` // 导出的账号列表。
	Total int                       `json:"total"` // 导出总数。
}

type externalAccountSyncItem struct {
	ID          int64          `json:"id"`          // 源实例账号 ID。
	Name        string         `json:"name"`        // 源实例账号名称。
	Platform    string         `json:"platform"`    // 账号平台。
	Type        string         `json:"type"`        // 账号类型。
	Status      string         `json:"status"`      // 账号状态。
	Credentials map[string]any `json:"credentials"` // 账号凭证。
	Extra       map[string]any `json:"extra"`       // 账号扩展信息。
	UpdatedAt   time.Time      `json:"updated_at"`  // 源实例更新时间。
}

type externalAccountSyncStats struct {
	fetched          int // 本轮拉取账号数。
	skippedEmail     int // 缺少 email 跳过数。
	created          int // 新增账号数。
	updated          int // 更新账号数。
	skippedDuplicate int // 本地重复命中跳过数。
	failed           int // 单账号处理失败数。
}

// NewExternalAccountSyncService 创建外部账号同步服务。
func NewExternalAccountSyncService(settings ExternalAccountSyncSettingReader, accountRepo ExternalAccountSyncAccountRepository, opts ExternalAccountSyncOptions) *ExternalAccountSyncService {
	interval := opts.Interval
	if interval <= 0 {
		interval = externalAccountSyncDefaultInterval
	}
	requestTimeout := opts.RequestTimeout
	if requestTimeout <= 0 {
		requestTimeout = externalAccountSyncDefaultRequestTimeout
	}
	return &ExternalAccountSyncService{
		settings:           settings,
		accountRepo:        accountRepo,
		client:             &http.Client{Timeout: requestTimeout},
		interval:           interval,
		requestTimeout:     requestTimeout,
		defaultConcurrency: opts.DefaultConcurrency,
		triggerCh:          make(chan string, 1),
		stopCh:             make(chan struct{}),
	}
}

// Start 启动外部账号同步服务。
func (s *ExternalAccountSyncService) Start() {
	if s == nil {
		return
	}
	s.wg.Add(1)
	go s.run()
}

// Stop 停止外部账号同步服务。
func (s *ExternalAccountSyncService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		close(s.stopCh)
		s.wg.Wait()
	})
}

// TriggerNow 触发一次立即同步。
func (s *ExternalAccountSyncService) TriggerNow(reason string) {
	if s == nil {
		return
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "manual"
	}
	select {
	case s.triggerCh <- reason:
	default:
	}
}

func (s *ExternalAccountSyncService) run() {
	defer s.wg.Done()

	s.syncWithBackgroundTimeout("startup")
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.syncWithBackgroundTimeout("scheduled")
		case reason := <-s.triggerCh:
			s.syncWithBackgroundTimeout(reason)
		case <-s.stopCh:
			return
		}
	}
}

func (s *ExternalAccountSyncService) syncWithBackgroundTimeout(reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), s.requestTimeout)
	defer cancel()
	if err := s.SyncOnce(ctx, reason); err != nil {
		log.Printf("[ExternalAccountSync] sync failed: reason=%s err=%v", reason, err)
	}
}

// SyncOnce 执行一次外部账号同步。
func (s *ExternalAccountSyncService) SyncOnce(ctx context.Context, reason string) error {
	if s == nil || s.settings == nil || s.accountRepo == nil {
		return nil
	}
	if !atomic.CompareAndSwapInt32(&s.running, 0, 1) {
		log.Printf("[ExternalAccountSync] skip overlapping sync: reason=%s", reason)
		return nil
	}
	defer atomic.StoreInt32(&s.running, 0)

	rawURL := strings.TrimSpace(s.settings.GetExternalAccountSyncURL(ctx))
	if rawURL == "" {
		return nil
	}
	if _, err := url.ParseRequestURI(rawURL); err != nil {
		log.Printf("[ExternalAccountSync] invalid sync url: url=%s err=%v", redactExternalAccountSyncURL(rawURL), err)
		return nil
	}

	payload, err := s.fetch(ctx, rawURL)
	if err != nil {
		return err
	}
	stats := externalAccountSyncStats{fetched: len(payload.Items)}
	for _, item := range payload.Items {
		if err := s.syncItem(ctx, item, &stats); err != nil {
			stats.failed++
			log.Printf("[ExternalAccountSync] sync item failed: platform=%s type=%s email=%s err=%v", item.Platform, item.Type, externalSyncItemEmail(item), err)
		}
	}
	log.Printf("[ExternalAccountSync] sync done: reason=%s fetched=%d skipped_email=%d created=%d updated=%d skipped_duplicate=%d failed=%d", reason, stats.fetched, stats.skippedEmail, stats.created, stats.updated, stats.skippedDuplicate, stats.failed)
	return nil
}

func (s *ExternalAccountSyncService) fetch(ctx context.Context, rawURL string) (*externalAccountSyncResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("token-export returned status %d from %s", resp.StatusCode, redactExternalAccountSyncURL(rawURL))
	}
	var payload externalAccountSyncResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

func (s *ExternalAccountSyncService) syncItem(ctx context.Context, item externalAccountSyncItem, stats *externalAccountSyncStats) error {
	email := externalSyncItemEmail(item)
	if email == "" {
		stats.skippedEmail++
		return nil
	}
	matches, err := s.accountRepo.ListByPlatformTypeCredentialEmail(ctx, item.Platform, item.Type, email)
	if err != nil {
		return err
	}
	switch len(matches) {
	case 0:
		account := s.accountFromItem(item, email)
		if err := s.accountRepo.Create(ctx, account); err != nil {
			return err
		}
		stats.created++
	case 1:
		account := matches[0]
		s.mergeItemIntoAccount(&account, item, email)
		if err := s.accountRepo.Update(ctx, &account); err != nil {
			return err
		}
		stats.updated++
	default:
		stats.skippedDuplicate++
		log.Printf("[ExternalAccountSync] duplicate local accounts skipped: platform=%s type=%s email=%s ids=%v", item.Platform, item.Type, email, externalAccountSyncIDs(matches))
	}
	return nil
}

func (s *ExternalAccountSyncService) accountFromItem(item externalAccountSyncItem, email string) *Account {
	account := &Account{
		Name:               item.Name,
		Platform:           item.Platform,
		Type:               item.Type,
		Status:             externalAccountSyncStatus(item.Status),
		Schedulable:        externalAccountSyncStatus(item.Status) == StatusActive,
		Credentials:        externalAccountSyncCredentials(item, email),
		Extra:              externalAccountSyncExtra(nil, item, email),
		Concurrency:        s.defaultConcurrency,
		Priority:           1,
		AutoPauseOnExpired: true,
	}
	return account
}

func (s *ExternalAccountSyncService) mergeItemIntoAccount(account *Account, item externalAccountSyncItem, email string) {
	account.Name = item.Name
	account.Platform = item.Platform
	account.Type = item.Type
	account.Status = externalAccountSyncStatus(item.Status)
	account.Credentials = externalAccountSyncCredentials(item, email)
	account.Extra = externalAccountSyncExtra(account.Extra, item, email)
}

func externalSyncItemEmail(item externalAccountSyncItem) string {
	if item.Credentials == nil {
		return ""
	}
	email, _ := item.Credentials["email"].(string)
	return strings.TrimSpace(email)
}

func externalAccountSyncStatus(status string) string {
	status = strings.TrimSpace(status)
	if status == "" {
		return StatusActive
	}
	return status
}

func externalAccountSyncCredentials(item externalAccountSyncItem, email string) map[string]any {
	credentials := cloneExternalAccountSyncMap(item.Credentials)
	if credentials == nil {
		credentials = make(map[string]any, 1)
	}
	credentials["email"] = email
	return credentials
}

func externalAccountSyncExtra(existing map[string]any, item externalAccountSyncItem, email string) map[string]any {
	out := cloneExternalAccountSyncMap(existing)
	if out == nil {
		out = make(map[string]any)
	}
	for k, v := range item.Extra {
		out[k] = v
	}
	out[externalTokenExportManagedKey] = true
	out[externalTokenExportEmailKey] = email
	out[externalTokenExportSourceIDKey] = float64(item.ID)
	if !item.UpdatedAt.IsZero() {
		out[externalTokenExportUpdatedAtKey] = item.UpdatedAt.Format(time.RFC3339)
	}
	return out
}

func externalAccountSyncIDs(accounts []Account) []int64 {
	ids := make([]int64, 0, len(accounts))
	for _, account := range accounts {
		ids = append(ids, account.ID)
	}
	return ids
}

func cloneExternalAccountSyncMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func redactExternalAccountSyncURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "<invalid>"
	}
	query := parsed.Query()
	for _, key := range []string{"password", "token", "secret", "api_key"} {
		if query.Has(key) {
			query.Set(key, "redacted")
		}
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}
