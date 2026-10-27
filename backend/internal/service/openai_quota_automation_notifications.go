package service

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// enqueueQuotaPauseNotifications 将管理员停调提醒交给固定邮件 worker，避免阻塞额度处理。
func (s *OpenAIQuotaAutoResetService) enqueueQuotaPauseNotifications(ctx context.Context, account *Account) {
	if s.quotaNotifier != nil {
		s.quotaNotifier.xtuisPush.NotifyQuotaPause(account)
	}
	if s.quotaNotifier == nil || s.notificationQueue == nil || s.quotaNotifier.notificationEmailService == nil {
		return
	}
	if !s.quotaNotifier.isAccountQuotaNotifyEnabled(ctx) {
		return
	}
	pause, ok := parseTempUnschedReasonPayload(account.TempUnschedulableReason)
	if !ok || pause.Source != AccountSchedulingThresholdReasonSource {
		return
	}
	eventKey := shortOpenAIAutoResetHash(account.TempUnschedulableReason)
	variables := map[string]string{
		"account_id": strconv.FormatInt(account.ID, 10), "account_name": account.Name,
		"platform": "OpenAI OAuth", "quota_dimension": pause.Window,
		"quota_used": fmt.Sprintf("%.2f%%", pause.UsedPercent), "quota_limit": "100%",
		"quota_remaining":         fmt.Sprintf("%.2f%%", math.Max(0, 100-pause.UsedPercent)),
		"quota_threshold":         fmt.Sprintf("%d%%", pause.ThresholdPercent),
		"quota_reset_description": "预计恢复 / Expected recovery (UTC): " + account.TempUnschedulableUntil.UTC().Format(time.RFC3339),
	}
	for _, email := range s.quotaNotifier.getAccountQuotaNotifyEmails(ctx) {
		input := NotificationEmailSendInput{
			Event: NotificationEmailEventAccountQuotaAlert, RecipientEmail: email,
			RecipientName: emailRecipientName(email), SourceType: "openai_quota_pause",
			SourceID: strconv.FormatInt(account.ID, 10), ReminderKey: eventKey, Variables: variables,
		}
		if err := s.notificationQueue.TryEnqueue(func() { s.sendQuotaPauseNotification(input) }); err != nil {
			slog.Warn("openai_quota_notification_queue_full", "account_id", account.ID, "error", err)
		}
	}
}

// sendQuotaPauseNotification 跨实例串行发送同一次停调的收件人提醒，成功结果可重放。
func (s *OpenAIQuotaAutoResetService) sendQuotaPauseNotification(input NotificationEmailSendInput) {
	ctx, cancel := context.WithTimeout(s.ctx, emailSendTimeout)
	defer cancel()
	if !s.quotaNotifier.isAccountQuotaNotifyEnabled(ctx) {
		return
	}
	key := "oqpn:" + input.SourceID + ":" + input.ReminderKey + ":" + shortOpenAIAutoResetHash(strings.ToLower(input.RecipientEmail))
	_, err := s.idempotency.Execute(ctx, IdempotencyExecuteOptions{
		Scope: "openai_quota_pause_notification", ActorScope: "account:" + input.SourceID,
		Method: http.MethodPost, Route: "/system/openai/quota-pause/notification",
		IdempotencyKey: key, Payload: key, TTL: openAIAutoResetAttemptTTL, RequireKey: true,
		ExecutionTimeout: emailSendTimeout,
	}, func(sendCtx context.Context) (any, error) {
		if err := s.quotaNotifier.notificationEmailService.Send(sendCtx, input); err != nil {
			return nil, err
		}
		return true, nil
	})
	if err != nil && infraerrors.Reason(err) != infraerrors.Reason(ErrIdempotencyInProgress) && infraerrors.Reason(err) != infraerrors.Reason(ErrIdempotencyRetryBackoff) {
		slog.Warn("openai_quota_pause_notification_failed", "account_id", input.SourceID, "error", err)
	}
}
