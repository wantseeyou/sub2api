package repository

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// SaveModelConsistencyResults 原子合并单次运行的模型结果，并拒绝旧任务和已变化配置。
func (r *accountRepository) SaveModelConsistencyResults(ctx context.Context, account *service.Account, snapshots map[string]service.ModelConsistencySnapshot, finishing bool) (bool, error) {
	payload, err := json.Marshal(snapshots)
	if err != nil {
		return false, err
	}
	credentials, err := json.Marshal(account.Credentials)
	if err != nil {
		return false, err
	}
	var proxyVersion *time.Time
	if account.Proxy != nil {
		proxyVersion = &account.Proxy.UpdatedAt
	}
	result, err := clientFromContext(ctx, r.client).ExecContext(ctx, `
		UPDATE accounts AS a
		SET extra = jsonb_set(COALESCE(a.extra, '{}'::jsonb), '{model_consistency_results}',
			COALESCE(a.extra -> 'model_consistency_results', '{}'::jsonb) || (
				SELECT COALESCE(jsonb_object_agg(candidate.key, candidate.value), '{}'::jsonb)
				FROM jsonb_each($2::jsonb) AS candidate
				WHERE NOT $5::boolean OR (
					a.extra -> 'model_consistency_results' -> candidate.key ->> 'run_id' = candidate.value ->> 'run_id'
					AND a.extra -> 'model_consistency_results' -> candidate.key ->> 'config_digest' = candidate.value ->> 'config_digest'
					AND a.extra -> 'model_consistency_results' -> candidate.key ->> 'status' = 'running'
					AND (candidate.value ->> 'deadline_at')::timestamptz > clock_timestamp()
				)
			), true)
		WHERE a.id = $1 AND a.deleted_at IS NULL AND a.updated_at = $3
		AND (NOT $5::boolean OR EXISTS (
			SELECT 1 FROM jsonb_each($2::jsonb) AS candidate
			WHERE a.extra -> 'model_consistency_results' -> candidate.key ->> 'run_id' = candidate.value ->> 'run_id'
			AND a.extra -> 'model_consistency_results' -> candidate.key ->> 'config_digest' = candidate.value ->> 'config_digest'
			AND a.extra -> 'model_consistency_results' -> candidate.key ->> 'status' = 'running'
			AND (candidate.value ->> 'deadline_at')::timestamptz > clock_timestamp()
		))
		AND EXISTS (SELECT 1 FROM accounts AS owner WHERE owner.id = COALESCE(a.parent_account_id, a.id) AND owner.deleted_at IS NULL AND owner.credentials = $6::jsonb)
		AND ($4::timestamptz IS NULL OR EXISTS (
			SELECT 1 FROM proxies AS p WHERE p.id = a.proxy_id AND p.updated_at = $4 AND p.deleted_at IS NULL
		))`, account.ID, string(payload), account.UpdatedAt, proxyVersion, finishing, string(credentials))
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows > 0, err
}

// CancelModelConsistencyRun 仅取消指定运行仍在执行的模型，不覆盖后续运行。
func (r *accountRepository) CancelModelConsistencyRun(ctx context.Context, accountID int64, runID string) (bool, error) {
	result, err := clientFromContext(ctx, r.client).ExecContext(ctx, `
		UPDATE accounts AS a
		SET extra = jsonb_set(COALESCE(a.extra, '{}'::jsonb), '{model_consistency_results}', (
			SELECT jsonb_object_agg(item.key, CASE
				WHEN item.value ->> 'run_id' = $2 AND item.value ->> 'status' = 'running'
				THEN item.value || jsonb_build_object('status', 'cancelled', 'reason', 'cancelled', 'completed_at', clock_timestamp())
				ELSE item.value END)
			FROM jsonb_each(a.extra -> 'model_consistency_results') AS item
		), true)
		WHERE a.id = $1 AND a.deleted_at IS NULL AND EXISTS (
			SELECT 1 FROM jsonb_each(COALESCE(a.extra -> 'model_consistency_results', '{}'::jsonb)) AS item
			WHERE item.value ->> 'run_id' = $2 AND item.value ->> 'status' = 'running'
			AND (item.value ->> 'deadline_at')::timestamptz > clock_timestamp()
		)`, accountID, runID)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows > 0, err
}
