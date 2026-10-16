package repository

import (
	"context"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/proxy"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// SaveTimezone 原子保存时区与检测结果，并拒绝覆盖探测期间已变更的代理。
func (r *proxyRepository) SaveTimezone(ctx context.Context, expected *service.Proxy, timezone *string, detection *domain.IPGeolocation) (*service.Proxy, error) {
	client := r.client
	var tx *dbent.Tx
	if contextTx := dbent.TxFromContext(ctx); contextTx != nil {
		client = contextTx.Client()
	} else {
		var err error
		tx, err = r.client.Tx(ctx)
		if err != nil && err != dbent.ErrTxStarted {
			return nil, err
		}
		if tx != nil {
			defer func() { _ = tx.Rollback() }()
			client = tx.Client()
		}
	}
	builder := client.Proxy.UpdateOneID(expected.ID).Where(proxy.UpdatedAtEQ(expected.UpdatedAt))
	if timezone != nil {
		builder.SetTimezone(*timezone)
	}
	if detection != nil {
		builder.SetTimezoneDetection(*detection)
	}
	updated, err := builder.Save(ctx)
	if dbent.IsNotFound(err) {
		return nil, infraerrors.Conflict("PROXY_CHANGED", "Proxy settings changed; refresh and try again")
	}
	if err != nil {
		return nil, err
	}
	if timezone != nil {
		// 通过 proxy_id 索引选取绑定账号，一次 SQL 生成有界批次；仅检测不刷新账号。
		_, err := client.ExecContext(ctx, `
			INSERT INTO scheduler_outbox (event_type, payload)
			SELECT $2, jsonb_build_object('account_ids', jsonb_agg(id ORDER BY id))
			FROM (
				SELECT id, (row_number() OVER (ORDER BY id) - 1) / $3 AS batch
				FROM accounts
				WHERE proxy_id = $1 AND deleted_at IS NULL
			) affected
			GROUP BY batch
		`, expected.ID, service.SchedulerOutboxEventAccountBulkChanged, proxyProbeOutboxAccountChunkSize)
		if err != nil {
			return nil, err
		}
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
	}
	return proxyEntityToService(updated), nil
}
