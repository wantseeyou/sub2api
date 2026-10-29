// Package groupview 提供独立的分组查看授权及白名单读取接口。
package groupview

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	apperrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/lib/pq"
)

type Group struct {
	ID   int64  `json:"id"`   // 分组标识。
	Name string `json:"name"` // 分组显示名称。
}

type Store struct {
	db *sql.DB // 复用应用数据库连接池。
}

// NewStore 创建独立的授权和只读查询存储。
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// Grants 每次从数据库读取仍然有效的授权，不使用权限缓存。
func (s *Store) Grants(ctx context.Context, userID int64) (groups []Group, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT g.id, g.name
        FROM user_group_view_grants v JOIN groups g ON g.id = v.group_id
        WHERE v.user_id = $1 AND g.deleted_at IS NULL ORDER BY g.id`, userID)
	if err != nil {
		return nil, fmt.Errorf("read group view grants: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	groups = make([]Group, 0)
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.Name); err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	return groups, rows.Err()
}

// Scope 拒绝任何未授权的显式分组，省略筛选时使用全部有效授权。
func Scope(grants []Group, selected []int64) ([]int64, error) {
	allowed := make([]int64, 0, len(grants))
	for _, g := range grants {
		allowed = append(allowed, g.ID)
	}
	if len(allowed) == 0 {
		return nil, apperrors.Forbidden("GROUP_VIEW_FORBIDDEN", "No group view grants")
	}
	for _, id := range selected {
		if !slices.Contains(allowed, id) {
			return nil, apperrors.Forbidden("GROUP_VIEW_FORBIDDEN", "Group is not authorized")
		}
	}
	if len(selected) > 0 {
		return selected, nil
	}
	return allowed, nil
}

// ReplaceGrants 锁定用户并原子替换集合，串行化同一用户的并发授权变更。
func (s *Store) ReplaceGrants(ctx context.Context, userID int64, ids []int64) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
				err = errors.Join(err, rollbackErr)
			}
		}
	}()
	var id int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`, userID).Scan(&id); err != nil {
		if err == sql.ErrNoRows {
			return apperrors.NotFound("USER_NOT_FOUND", "User not found")
		}
		return err
	}
	ids = slices.Clone(ids)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if len(ids) > 0 {
		rows, err := tx.QueryContext(ctx, `SELECT id FROM groups WHERE id = ANY($1::bigint[]) AND deleted_at IS NULL FOR SHARE`, pq.Array(ids))
		if err != nil {
			return err
		}
		count := 0
		for rows.Next() {
			if err := rows.Scan(&id); err != nil {
				return errors.Join(err, rows.Close())
			}
			count++
		}
		err = errors.Join(rows.Err(), rows.Close())
		if err != nil {
			return err
		}
		if count != len(ids) {
			return apperrors.BadRequest("INVALID_GROUP", "Group does not exist")
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_group_view_grants WHERE user_id = $1`, userID); err != nil {
		return err
	}
	if len(ids) > 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO user_group_view_grants (user_id, group_id)
            SELECT $1, unnest($2::bigint[])`, userID, pq.Array(ids)); err != nil {
			return err
		}
	}
	return tx.Commit()
}
