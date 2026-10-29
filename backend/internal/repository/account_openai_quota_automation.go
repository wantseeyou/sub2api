package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// ListOpenAIQuotaAutomationCandidateIDs 按主键分页查询旧策略、额度停调及临期保底用卡账号。
func (r *accountRepository) ListOpenAIQuotaAutomationCandidateIDs(ctx context.Context, afterID int64, limit int, includePaused, includeActive bool) ([]int64, error) {
	query := r.client.Account.Query().Where(
		dbaccount.IDGT(afterID), dbaccount.PlatformEQ(service.PlatformOpenAI),
		dbaccount.TypeEQ(service.AccountTypeOAuth), dbaccount.StatusEQ(service.StatusActive),
		dbaccount.SchedulableEQ(true), dbaccount.ParentAccountIDIsNil(),
	)
	query.Where(func(s *entsql.Selector) {
		now := time.Now()
		if includeActive {
			s.Where(entsql.Or(entsql.IsNull(s.C(dbaccount.FieldExpiresAt)), entsql.GT(s.C(dbaccount.FieldExpiresAt), now), entsql.EQ(s.C(dbaccount.FieldAutoPauseOnExpired), false)))
			return
		}
		legacy := entsql.And(
			entsql.ExprP("extra ->> 'auto_reset_credit_enabled' IN ('true', '1')"),
			entsql.Or(entsql.IsNull(s.C(dbaccount.FieldRateLimitResetAt)), entsql.LTE(s.C(dbaccount.FieldRateLimitResetAt), now)),
			entsql.Or(entsql.IsNull(s.C(dbaccount.FieldTempUnschedulableUntil)), entsql.LTE(s.C(dbaccount.FieldTempUnschedulableUntil), now)),
		)
		if includePaused {
			s.Where(entsql.Or(legacy, entsql.And(
				entsql.GT(s.C(dbaccount.FieldTempUnschedulableUntil), now),
				entsql.Contains(s.C(dbaccount.FieldTempUnschedulableReason), service.AccountSchedulingThresholdReasonSource),
			)))
		} else {
			s.Where(legacy)
		}
	})
	return query.Order(dbaccount.ByID()).Limit(limit).IDs(ctx)
}

// CompareAndSwapOpenAIAutoResetState 仅替换本次观察到的核销状态，防止跨卡并发回写丢失待确认结果。
func (r *accountRepository) CompareAndSwapOpenAIAutoResetState(ctx context.Context, id int64, previous any, state *service.OpenAIAutoResetCreditState) (bool, error) {
	before, err := json.Marshal(previous)
	if err != nil {
		return false, fmt.Errorf("marshal previous reset state: %w", err)
	}
	after, err := json.Marshal(state)
	if err != nil {
		return false, fmt.Errorf("marshal next reset state: %w", err)
	}
	result, err := r.sql.ExecContext(ctx, `UPDATE accounts
		SET extra = COALESCE(extra, '{}'::jsonb) || jsonb_build_object($2::text, $4::jsonb), updated_at = NOW()
		WHERE id = $1 AND deleted_at IS NULL
			AND COALESCE(extra -> $2::text, 'null'::jsonb) = $3::jsonb`,
		id, service.OpenAIAutoResetCreditStateExtraKey, string(before), string(after))
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		return false, err
	}
	r.syncSchedulerAccountSnapshotDetached(ctx, id)
	return true, nil
}

// UpdateOpenAIQuotaAutomationSnapshot 原子复核查询快照，更新额度并按需解除匹配的停调。
func (r *accountRepository) UpdateOpenAIQuotaAutomationSnapshot(ctx context.Context, account *service.Account, updates map[string]any, clearPause bool) (changed bool, err error) {
	payload, err := json.Marshal(updates)
	if err != nil {
		return false, fmt.Errorf("marshal OpenAI quota snapshot: %w", err)
	}
	credentials, err := json.Marshal(account.Credentials)
	if err != nil {
		return false, fmt.Errorf("marshal OpenAI quota identity: %w", err)
	}
	extra, err := json.Marshal(account.Extra)
	if err != nil {
		return false, fmt.Errorf("marshal previous OpenAI quota snapshot: %w", err)
	}
	rows, err := r.sql.QueryContext(ctx, `
		WITH updated AS (
			UPDATE accounts AS a
			SET extra = COALESCE(a.extra, '{}'::jsonb) || $2::jsonb,
				temp_unschedulable_until = CASE WHEN $9 THEN NULL ELSE a.temp_unschedulable_until END,
				temp_unschedulable_reason = CASE WHEN $9 THEN NULL ELSE a.temp_unschedulable_reason END,
				updated_at = NOW()
			WHERE a.id = $1 AND a.deleted_at IS NULL
				AND a.platform = 'openai' AND a.type = 'oauth'
				AND a.status = 'active' AND a.schedulable IS TRUE
				AND a.parent_account_id IS NULL
				AND (a.auto_pause_on_expired IS NOT TRUE OR a.expires_at IS NULL OR a.expires_at > NOW())
				AND a.credentials = $3::jsonb
				AND a.proxy_id IS NOT DISTINCT FROM $4
				AND a.temp_unschedulable_until IS NOT DISTINCT FROM $5
				AND COALESCE(a.temp_unschedulable_reason, '') = $6
				AND a.extra -> 'codex_usage_updated_at' IS NOT DISTINCT FROM $7::jsonb -> 'codex_usage_updated_at'
				AND a.extra -> 'codex_7d_zero_recovery' IS NOT DISTINCT FROM $7::jsonb -> 'codex_7d_zero_recovery'
				AND a.rate_limit_reset_at IS NOT DISTINCT FROM $10
				AND a.overload_until IS NOT DISTINCT FROM $11
			RETURNING a.id
		), emitted AS (
			INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)
			SELECT $8, id, NULL, NULL FROM updated WHERE $9
		)
		SELECT id FROM updated
	`, account.ID, string(payload), string(credentials), account.ProxyID,
		account.TempUnschedulableUntil, account.TempUnschedulableReason, string(extra),
		service.SchedulerOutboxEventAccountChanged, clearPause, account.RateLimitResetAt, account.OverloadUntil)
	if err != nil {
		return false, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	if !rows.Next() {
		return false, rows.Err()
	}
	var id int64
	if err := rows.Scan(&id); err != nil {
		return false, err
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	if clearPause {
		r.syncSchedulerAccountSnapshotDetached(ctx, id)
	}
	return true, nil
}
