package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	SettingKeyXtuisPushEnabled             = "xtuis_push_enabled"
	SettingKeyXtuisPushAccountQuotaEnabled = "xtuis_push_account_quota_enabled"
	SettingKeyXtuisPushAccountErrorEnabled = "xtuis_push_account_error_enabled"
	SettingKeyXtuisPushNewUserEnabled      = "xtuis_push_new_user_enabled"
	SettingKeyXtuisPushTokens              = "xtuis_push_tokens"
	xtuisPushTimeout                       = 10 * time.Second
	xtuisPushMaxTokens                     = 100
)

var xtuisPushTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)

// NormalizeXtuisPushTokens 校验并去重 Token，不在错误中回显凭据。
func NormalizeXtuisPushTokens(tokens []string) ([]string, error) {
	result := make([]string, 0, len(tokens))
	seen := make(map[string]bool, len(tokens))
	for _, raw := range tokens {
		token := strings.TrimSpace(raw)
		if token == "" || seen[token] {
			continue
		}
		if !xtuisPushTokenPattern.MatchString(token) {
			return nil, infraerrors.BadRequest("INVALID_XTUIS_PUSH_TOKEN", "Enter the Xtuis token only, using letters, numbers, underscores or hyphens (up to 256 characters).")
		}
		seen[token] = true
		result = append(result, token)
	}
	if len(result) > xtuisPushMaxTokens {
		return nil, infraerrors.BadRequest("TOO_MANY_XTUIS_PUSH_TOKENS", "At most 100 distinct Xtuis tokens are supported.")
	}
	return result, nil
}

// parseXtuisPushTokens 解析已保存的 Token 列表，空配置视为未配置。
func parseXtuisPushTokens(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var tokens []string
	if err := json.Unmarshal([]byte(raw), &tokens); err != nil {
		return nil, fmt.Errorf("invalid stored Xtuis token list")
	}
	return NormalizeXtuisPushTokens(tokens)
}

// XtuisPushService 使用固定 worker 向多个 Token 并发发送系统通知。
type XtuisPushService struct {
	settings    SettingRepository             // 独立推送开关与 Token 配置。
	accounts    AccountQuotaReader            // 账号名称与邮箱读取接口。
	idempotency *IdempotencyCoordinator       // 同一事件和 Token 的跨实例去重。
	client      *http.Client                  // 固定超时且禁止重定向的发送客户端。
	events      *SubscriptionMaintenanceQueue // 有界事件队列，负责配置读取和内容准备。
	deliveries  *SubscriptionMaintenanceQueue // 有界并发发送队列。
}

// NewXtuisPushService 创建独立于 SMTP 的虾推啥推送服务。
func NewXtuisPushService(settings SettingRepository, accounts AccountQuotaReader, idempotency *IdempotencyCoordinator) *XtuisPushService {
	return &XtuisPushService{
		settings: settings, accounts: accounts, idempotency: idempotency,
		client: &http.Client{Timeout: xtuisPushTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		events: NewSubscriptionMaintenanceQueue(1, 100), deliveries: NewSubscriptionMaintenanceQueue(4, 100),
	}
}

// Stop 等待已接收事件展开后，再关闭发送队列。
func (s *XtuisPushService) Stop() {
	s.events.Stop()
	s.deliveries.Stop()
}

// IsAccountQuotaEnabled 供分钟扫描判断是否需要补偿额度停调通知。
func (s *XtuisPushService) IsAccountQuotaEnabled(ctx context.Context) bool {
	if s == nil {
		return false
	}
	tokens, err := s.tokens(ctx, SettingKeyXtuisPushAccountQuotaEnabled)
	if err != nil {
		slog.Warn("xtuis_push_settings_failed", "error", err)
	}
	return len(tokens) > 0
}

// tokens 一次读取总开关、分类开关与 Token，未启用时不发送。
func (s *XtuisPushService) tokens(ctx context.Context, event string) ([]string, error) {
	values, err := s.settings.GetMultiple(ctx, []string{SettingKeyXtuisPushEnabled, event, SettingKeyXtuisPushTokens})
	if err != nil {
		return nil, err
	}
	if values[SettingKeyXtuisPushEnabled] != "true" || values[event] != "true" {
		return nil, nil
	}
	return parseXtuisPushTokens(values[SettingKeyXtuisPushTokens])
}

// enqueue 在后台准备通知，并将各 Token 的发送任务交给固定 worker。
func (s *XtuisPushService) enqueue(event, eventKey string, render func(context.Context) (string, string, error)) {
	if s == nil {
		return
	}
	if err := s.events.TryEnqueue(func() {
		ctx, cancel := context.WithTimeout(context.Background(), xtuisPushTimeout)
		defer cancel()
		tokens, err := s.tokens(ctx, event)
		if err != nil {
			slog.Warn("xtuis_push_settings_failed", "event", event, "error", err)
			return
		}
		if len(tokens) == 0 {
			return
		}
		title, body, err := render(ctx)
		if err != nil {
			slog.Warn("xtuis_push_message_failed", "event", event, "error", err)
			return
		}
		for _, token := range tokens {
			if err := s.deliveries.Enqueue(func() { s.send(event, eventKey, token, title, body) }); err != nil {
				slog.Warn("xtuis_push_delivery_queue_closed", "event", event, "error", err)
			}
		}
	}); err != nil {
		slog.Warn("xtuis_push_queue_full", "event", event, "error", err)
	}
}

// send 发出请求前复核开关和 Token，成功事件不重复发送。
func (s *XtuisPushService) send(event, eventKey, token, title, body string) {
	ctx, cancel := context.WithTimeout(context.Background(), xtuisPushTimeout)
	defer cancel()
	tokens, err := s.tokens(ctx, event)
	if err != nil {
		slog.Warn("xtuis_push_settings_failed", "event", event, "error", err)
		return
	}
	if !slices.Contains(tokens, token) {
		return
	}
	key := "xtuis:" + HashIdempotencyKey(event+":"+eventKey+":"+token)
	_, err = s.idempotency.Execute(ctx, IdempotencyExecuteOptions{
		Scope: "xtuis_push", ActorScope: "system", Method: http.MethodPost, Route: "/system/xtuis-push",
		IdempotencyKey: key, Payload: key, RequireKey: true, TTL: 8 * 24 * time.Hour, ExecutionTimeout: xtuisPushTimeout,
	}, func(sendCtx context.Context) (any, error) {
		return s.post(sendCtx, token, title, body)
	})
	if err != nil && infraerrors.Reason(err) != infraerrors.Reason(ErrIdempotencyInProgress) && infraerrors.Reason(err) != infraerrors.Reason(ErrIdempotencyRetryBackoff) {
		slog.Warn("xtuis_push_send_failed", "event", event, "token_hash", HashIdempotencyKey(token), "error", err)
	}
}

// post 使用固定 HTTPS 接口发送纯文本，错误信息不包含 Token 或响应正文。
func (s *XtuisPushService) post(ctx context.Context, token, title, body string) (string, error) {
	form := url.Values{"text": {title}, "desp": {body}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://wx.xtuis.cn/"+url.PathEscape(token)+".send", strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("could not build Xtuis request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("xtuis request failed (%T)", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("xtuis returned HTTP %d", resp.StatusCode)
	}
	var result struct {
		Code      int    `json:"code"`   // 平台业务状态码。
		MessageID string `json:"msg_id"` // 平台接受消息后返回的标识。
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&result); err != nil {
		return "", fmt.Errorf("invalid Xtuis response")
	}
	if result.Code != http.StatusOK || result.MessageID == "" {
		return "", fmt.Errorf("xtuis rejected the notification (code %d)", result.Code)
	}
	return result.MessageID, nil
}
