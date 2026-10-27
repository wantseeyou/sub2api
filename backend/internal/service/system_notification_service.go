package service

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/util/logredact"
	"github.com/google/uuid"
)

const (
	SettingKeyAccountErrorNotifyEnabled = "account_error_notify_enabled"
	SettingKeyNewUserNotifyEnabled      = "new_user_notify_enabled"
)

var systemNotificationAuthPattern = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[a-z0-9._~+/-]+=*`)

// SystemNotificationService 使用固定 worker 向管理员发送系统事件通知。
type SystemNotificationService struct {
	settings    SettingRepository             // 通知开关与共用收件地址。
	accounts    AccountQuotaReader            // 错误账号的基础信息读取接口。
	email       *NotificationEmailService     // 共用的模板与 SMTP 发信服务。
	xtuisPush   *XtuisPushService             // 独立于邮件开关的微信推送。
	idempotency *IdempotencyCoordinator       // 同一事件与收件人的发送去重。
	queue       *SubscriptionMaintenanceQueue // 有界事件处理队列。
	deliveries  *SubscriptionMaintenanceQueue // 固定发信 worker 与有界收件人队列。
}

// NewSystemNotificationService 创建系统服务通知队列。
func NewSystemNotificationService(settings SettingRepository, accounts AccountQuotaReader, email *NotificationEmailService, idempotency *IdempotencyCoordinator) *SystemNotificationService {
	return &SystemNotificationService{settings: settings, accounts: accounts, email: email, idempotency: idempotency, queue: NewSubscriptionMaintenanceQueue(1, 100), deliveries: NewSubscriptionMaintenanceQueue(2, 100)}
}

// Stop 等待已接收的系统通知处理结束。
func (s *SystemNotificationService) Stop() {
	s.queue.Stop()
	s.deliveries.Stop()
}

// NotifyAccountError 记录一次账号进入错误状态的事件，避免阻塞网关请求。
func (s *SystemNotificationService) NotifyAccountError(accountID int64, errorMessage string) {
	if s == nil || accountID <= 0 {
		return
	}
	eventID, occurredAt := uuid.NewString(), time.Now()
	s.xtuisPush.NotifyAccountError(accountID, errorMessage, eventID, occurredAt)
	errorMessage = systemNotificationAuthPattern.ReplaceAllString(errorMessage, "$1 ***")
	s.enqueue(SettingKeyAccountErrorNotifyEnabled, NotificationEmailSendInput{
		Event: NotificationEmailEventAccountError, SourceType: "account_error", SourceID: strconv.FormatInt(accountID, 10), ReminderKey: eventID,
		Variables: map[string]string{"account_id": strconv.FormatInt(accountID, 10), "error_message": truncateForLog([]byte(logredact.RedactText(errorMessage, "api_key", "authorization", "x-api-key")), 1000), "event_time": time.Now().UTC().Format(time.RFC3339)},
	})
}

// NotifyUserRegistered 记录注册完成的新用户，已有用户登录不调用此入口。
func (s *SystemNotificationService) NotifyUserRegistered(user *User, source string) {
	if s == nil || user == nil || user.ID <= 0 {
		return
	}
	s.xtuisPush.NotifyUserRegistered(user)
	s.enqueue(SettingKeyNewUserNotifyEnabled, NotificationEmailSendInput{
		Event: NotificationEmailEventNewUserRegistered, SourceType: "user_registration", SourceID: strconv.FormatInt(user.ID, 10), ReminderKey: "registered",
		Variables: map[string]string{"user_id": strconv.FormatInt(user.ID, 10), "user_email": user.Email, "user_name": user.Username, "signup_source": source, "event_time": time.Now().UTC().Format(time.RFC3339)},
	})
}

// recipients 一次读取分类开关与收件地址，保留旧限额通知的邮箱配置。
func (s *SystemNotificationService) recipients(ctx context.Context, settingKey string) ([]string, error) {
	settings, err := s.settings.GetMultiple(ctx, []string{settingKey, SettingKeyAccountQuotaNotifyEmails})
	if err != nil {
		return nil, err
	}
	if settings[settingKey] != "true" {
		return nil, nil
	}
	return filterVerifiedEmails(ParseNotifyEmails(settings[SettingKeyAccountQuotaNotifyEmails])), nil
}

// enqueue 将事件展开为独立收件人任务，循环中只入队，发信由固定 worker 执行。
func (s *SystemNotificationService) enqueue(settingKey string, input NotificationEmailSendInput) {
	if err := s.queue.TryEnqueue(func() {
		ctx, cancel := context.WithTimeout(context.Background(), emailSendTimeout)
		defer cancel()
		recipients, err := s.recipients(ctx, settingKey)
		if err != nil {
			slog.Warn("system_notification_settings_failed", "event", input.Event, "error", err)
			return
		}
		if len(recipients) == 0 {
			return
		}
		if input.Event == NotificationEmailEventAccountError {
			id, err := strconv.ParseInt(input.SourceID, 10, 64)
			if err != nil {
				slog.Error("system_notification_account_id_invalid", "error", err)
				return
			}
			account, err := s.accounts.GetByID(ctx, id)
			if err != nil {
				slog.Warn("system_notification_account_load_failed", "account_id", id, "error", err)
				return
			}
			if account == nil {
				return
			}
			input.Variables["account_name"] = account.Name
			input.Variables["platform"] = account.Platform
		}
		for _, recipient := range recipients {
			delivery := input
			delivery.RecipientEmail = recipient
			delivery.RecipientName = emailRecipientName(recipient)
			if err := s.deliveries.TryEnqueue(func() { s.send(settingKey, delivery) }); err != nil {
				slog.Warn("system_notification_queue_full", "event", input.Event, "source_id", input.SourceID, "error", err)
			}
		}
	}); err != nil {
		slog.Warn("system_notification_queue_full", "event", input.Event, "source_id", input.SourceID, "error", err)
	}
}

// send 复核当前开关和收件地址，并持久化同一事件的发信结果。
func (s *SystemNotificationService) send(settingKey string, input NotificationEmailSendInput) {
	ctx, cancel := context.WithTimeout(context.Background(), emailSendTimeout)
	defer cancel()
	recipients, err := s.recipients(ctx, settingKey)
	if err != nil {
		slog.Warn("system_notification_settings_failed", "event", input.Event, "error", err)
		return
	}
	if !slices.Contains(recipients, input.RecipientEmail) {
		return
	}
	key := "sysmail:" + notificationEmailHash(fmt.Sprintf("%s:%s:%s:%s", input.Event, input.SourceID, input.ReminderKey, input.RecipientEmail))
	_, err = s.idempotency.Execute(ctx, IdempotencyExecuteOptions{
		Scope: "system_notification", ActorScope: "system", Method: http.MethodPost, Route: "/system/notifications", IdempotencyKey: key, Payload: key,
		RequireKey: true, TTL: 8 * 24 * time.Hour, ExecutionTimeout: emailSendTimeout,
	}, func(sendCtx context.Context) (any, error) {
		if err := s.email.Send(sendCtx, input); err != nil {
			return nil, err
		}
		return true, nil
	})
	if err != nil && infraerrors.Reason(err) != infraerrors.Reason(ErrIdempotencyInProgress) && infraerrors.Reason(err) != infraerrors.Reason(ErrIdempotencyRetryBackoff) {
		slog.Warn("system_notification_send_failed", "event", input.Event, "source_id", input.SourceID, "error", err)
	}
}

// NotifyUserRegistered 通知管理员用户已完成注册；在 OAuth 最终提交后调用。
func (s *AuthService) NotifyUserRegistered(user *User, source string) {
	s.systemNotifications.NotifyUserRegistered(user, source)
}
