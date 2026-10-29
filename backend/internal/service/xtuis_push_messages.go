package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/util/logredact"
	"github.com/google/uuid"
)

// NotifyAccountError 发送实际进入错误状态的事件，使用触发时刻而非账号后续更新时间。
func (s *XtuisPushService) NotifyAccountError(accountID int64, message, eventID string, occurredAt time.Time) {
	if s == nil {
		return
	}
	message = systemNotificationAuthPattern.ReplaceAllString(message, "$1 ***")
	message = logredact.RedactText(message, "api_key", "authorization", "x-api-key")
	timeLabel := "发生时间："
	if occurredAt.IsZero() {
		occurredAt = time.Now()
		timeLabel = "检测时间："
	}
	s.enqueue(SettingKeyXtuisPushAccountErrorEnabled, fmt.Sprintf("%d:%s", accountID, eventID), func(ctx context.Context) (string, string, error) {
		account, err := s.accounts.GetByID(ctx, accountID)
		if err != nil {
			return "", "", err
		}
		if account == nil {
			return "", "", ErrAccountNotFound
		}
		kind, title := "账号错误", "账号错误"
		if strings.Contains(message, "401") {
			kind, title = "401 认证失败", "账号401"
		}
		lines := xtuisPushAccountLines(account)
		lines = append(lines, "", "错误类型："+kind)
		if message != "" {
			lines = append(lines, "错误原因："+xtuisPushText(message, 500))
		}
		lines = append(lines, "账号状态：错误，已停止调度", "", timeLabel+xtuisPushTime(occurredAt))
		return xtuisPushTitle(title, account.Name), strings.Join(lines, "\n"), nil
	})
}

// NotifyUserRegistered 发送注册成功通知，名称为空时不显示名称行。
func (s *XtuisPushService) NotifyUserRegistered(user *User) {
	if s == nil || user == nil || user.ID <= 0 {
		return
	}
	lines := []string{"账号：" + xtuisPushText(user.Email, 255)}
	if strings.TrimSpace(user.Username) != "" {
		lines = append(lines, "名称："+xtuisPushText(user.Username, 100))
	}
	lines = append(lines, "注册时间："+xtuisPushTime(time.Now()))
	body := strings.Join(lines, "\n")
	s.enqueue(SettingKeyXtuisPushNewUserEnabled, strconv.FormatInt(user.ID, 10), func(context.Context) (string, string, error) {
		return "有新用户注册", body, nil
	})
}

// NotifyQuotaPause 按停调事件去重，附带已有额度重置及重置卡快照。
func (s *XtuisPushService) NotifyQuotaPause(account *Account) {
	if s == nil || account == nil {
		return
	}
	pause, ok := parseTempUnschedReasonPayload(account.TempUnschedulableReason)
	if !ok || pause.Source != AccountSchedulingThresholdReasonSource {
		return
	}
	window := map[string]string{"5h": "5 小时", "7d": "7 日"}[pause.Window]
	if window == "" {
		window = xtuisPushText(pause.Window, 50)
	}
	lines := xtuisPushAccountLines(account)
	lines = append(lines, "", "触发额度："+window+"额度", fmt.Sprintf("当前已用：%g%%", pause.UsedPercent),
		fmt.Sprintf("停调阈值：%d%%", pause.ThresholdPercent), "账号状态：临时不可调度")
	if resetAt, err := time.Parse(time.RFC3339, account.getExtraString("codex_"+pause.Window+"_reset_at")); err == nil {
		lines = append(lines, "", window+"额度重置："+xtuisPushTime(resetAt))
	} else if account.TempUnschedulableUntil != nil {
		lines = append(lines, "", "预计恢复："+xtuisPushTime(*account.TempUnschedulableUntil))
	}
	lines = append(lines, xtuisPushResetCreditLines(account)...)
	lines = append(lines, "", "通知时间："+xtuisPushTime(time.Now()))
	title, body := xtuisPushTitle("额度停调", account.Name), strings.Join(lines, "\n")
	eventKey := fmt.Sprintf("openai:%d:%s", account.ID, HashIdempotencyKey(account.TempUnschedulableReason))
	s.enqueue(SettingKeyXtuisPushAccountQuotaEnabled, eventKey, func(context.Context) (string, string, error) { return title, body, nil })
}

// NotifyAutoResetCreditUsed 按核销周期和卡片指纹去重，推送实际用卡结果及本地恢复情况。
func (s *XtuisPushService) NotifyAutoResetCreditUsed(account *Account, state *OpenAIAutoResetCreditState, expiresAt string, windowsReset int, post OpenAIQuotaResetPostProcessResult) {
	if s == nil || account == nil || state == nil {
		return
	}
	remaining := max(0, state.AvailableCount-1)
	remainingLabel := "预计剩余可用卡"
	if post.Quota != nil && post.Quota.RateLimitResetCredits != nil {
		remaining = post.Quota.RateLimitResetCredits.AvailableCount
		remainingLabel = "剩余可用卡"
	}
	lines := xtuisPushAccountLines(account)
	lines = append(lines, "", "使用结果：已自动使用 1 张重置卡")
	if window := map[string]string{"5h": "5 小时", "7d": "7 日", "5h+7d": "5 小时、7 日"}[state.TriggerWindow]; window != "" {
		lines = append(lines, "触发额度："+window+"额度")
	}
	if expires, err := time.Parse(time.RFC3339, expiresAt); err == nil {
		lines = append(lines, "卡片原到期："+xtuisPushTime(expires))
	}
	if windowsReset > 0 {
		lines = append(lines, fmt.Sprintf("重置窗口：%d 个", windowsReset))
	}
	lines = append(lines, fmt.Sprintf("%s：%d 张", remainingLabel, remaining))
	if !post.AccountStateRecovered || post.WarningCode != "" {
		lines = append(lines, "后续处理：本地恢复未完成，请检查账号状态和额度")
	}
	lines = append(lines, "", "确认时间："+xtuisPushTime(time.Now()))
	title, body := xtuisPushTitle("自动用卡", account.Name), strings.Join(lines, "\n")
	eventKey := fmt.Sprintf("reset-credit:%d:%s:%s", account.ID, state.AttemptCycleHash, state.AttemptCreditHash)
	s.enqueue(SettingKeyXtuisPushAccountQuotaEnabled, eventKey, func(context.Context) (string, string, error) { return title, body, nil })
}

// NotifyAccountQuota 在金额限额跨过告警阈值时推送，不将提前告警误报为停调。
func (s *XtuisPushService) NotifyAccountQuota(account *Account, dim quotaDim, used, threshold float64) {
	if s == nil || account == nil {
		return
	}
	label := map[string]string{quotaDimDaily: "日金额限额", quotaDimWeekly: "周金额限额", quotaDimTotal: "总金额限额"}[dim.name]
	lines := xtuisPushAccountLines(account)
	lines = append(lines, "", "触发限额："+label, fmt.Sprintf("当前已用：$%.2f", used),
		fmt.Sprintf("总额度：$%.2f", dim.limit), fmt.Sprintf("告警用量阈值：$%.2f", threshold))
	if resetAt, err := time.Parse(time.RFC3339, account.getExtraString("quota_"+dim.name+"_reset_at")); err == nil {
		lines = append(lines, "", "下次重置："+xtuisPushTime(resetAt))
	}
	lines = append(lines, xtuisPushResetCreditLines(account)...)
	lines = append(lines, "", "通知时间："+xtuisPushTime(time.Now()))
	title, body := xtuisPushTitle("账号限额", account.Name), strings.Join(lines, "\n")
	eventKey := fmt.Sprintf("amount:%d:%s:%s", account.ID, dim.name, uuid.NewString())
	s.enqueue(SettingKeyXtuisPushAccountQuotaEnabled, eventKey, func(context.Context) (string, string, error) { return title, body, nil })
}

// xtuisPushAccountLines 只展示实际存在的账号名称和邮箱。
func xtuisPushAccountLines(account *Account) []string {
	lines := make([]string, 0, 2)
	if strings.TrimSpace(account.Name) != "" {
		lines = append(lines, "账号名称："+xtuisPushText(account.Name, 200))
	}
	if email := strings.TrimSpace(account.GetCredential("email")); email != "" {
		lines = append(lines, "邮箱："+xtuisPushText(email, 255))
	}
	return lines
}

// xtuisPushResetCreditLines 复用上游用量查询保存的卡片信息，未知数量不当作零。
func xtuisPushResetCreditLines(account *Account) []string {
	raw := account.Extra[openaiQuotaResetCreditsKey]
	if raw == nil || account.Platform != PlatformOpenAI {
		return nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		slog.Warn("xtuis_push_reset_credits_invalid", "account_id", account.ID)
		return nil
	}
	var credits struct {
		AvailableCount *int                               `json:"available_count"` // 已知的可用卡数量，缺省为未知。
		Credits        []OpenAIRateLimitResetCreditDetail `json:"credits"`         // 缓存中的卡片到期信息。
	}
	if err := json.Unmarshal(data, &credits); err != nil {
		slog.Warn("xtuis_push_reset_credits_invalid", "account_id", account.ID)
		return nil
	}
	if credits.AvailableCount == nil || *credits.AvailableCount < 0 {
		return nil
	}
	now := time.Now()
	available := 0
	var earliest time.Time
	for _, credit := range credits.Credits {
		if strings.TrimSpace(credit.ExpiresAt) == "" {
			continue
		}
		expiry, err := time.Parse(time.RFC3339, credit.ExpiresAt)
		if err == nil && !expiry.After(now) {
			continue
		}
		available++
		if err == nil && (earliest.IsZero() || expiry.Before(earliest)) {
			earliest = expiry
		}
	}
	available = min(*credits.AvailableCount, available)
	// 缓存声称有卡但到期信息已不可用时，省略未知数量，等待下次上游查询更新。
	if *credits.AvailableCount > 0 && available == 0 {
		return nil
	}
	lines := []string{fmt.Sprintf("可用重置卡：%d 张", available)}
	if available > 0 && !earliest.IsZero() {
		lines = append(lines, "重置卡最早到期："+xtuisPushTime(earliest))
	}
	return lines
}

// xtuisPushTime 使用服务时区显示分钟精度时间，并标明 UTC 偏移。
func xtuisPushTime(at time.Time) string {
	return at.In(timezone.Location()).Format("2006-01-02 15:04 -07:00")
}

// xtuisPushText 将动态值限制在单行内，避免空行或控制字符破坏正文结构。
func xtuisPushText(value string, limit int) string {
	text := []rune(strings.Join(strings.Fields(value), " "))
	if len(text) > limit {
		text = append(text[:limit], '…')
	}
	return string(text)
}

// xtuisPushTitle 保留事件类型，并将微信卡片标题限制在约 17 字以内。
func xtuisPushTitle(kind, name string) string {
	if strings.TrimSpace(name) != "" {
		kind += "｜" + strings.TrimSpace(name)
	}
	return xtuisPushText(kind, 16)
}
