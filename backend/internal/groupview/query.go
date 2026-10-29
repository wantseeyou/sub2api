package groupview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

type Filter struct {
	Groups      []int64   // 本次请求已校验的分组集合。
	Start       time.Time // 包含的开始时刻。
	End         time.Time // 不包含的结束时刻。
	UserID      int64     // 可选人员筛选。
	APIKeyID    int64     // 可选用户密钥筛选。
	AccountID   int64     // 可选上游账号筛选。
	Model       string    // 可选模型筛选。
	RequestType string    // 可选请求类型筛选。
	Search      string    // 当前列表的名称搜索。
	Platform    string    // 账号平台筛选。
	Type        string    // 账号类型筛选。
	Status      string    // 账号状态筛选。
	Sort        string    // 当前列表的白名单排序字段。
	Desc        bool      // 是否倒序。
	Page        int       // 页码，从一开始。
	PageSize    int       // 每页条数。
	Timezone    string    // 趋势图使用的有效时区。
}

const tokenSum = `(ul.input_tokens::bigint + ul.output_tokens + ul.cache_creation_tokens + ul.cache_read_tokens)`
const requestType = `(CASE WHEN ul.request_type = 0 THEN CASE WHEN ul.openai_ws_mode THEN 'ws_v2' WHEN ul.stream THEN 'stream' ELSE 'sync' END
    ELSE CASE ul.request_type WHEN 1 THEN 'sync' WHEN 2 THEN 'stream' WHEN 3 THEN 'ws_v2' WHEN 4 THEN 'cyber' WHEN 5 THEN 'live' ELSE 'unknown' END END)`

// usageWhere 将全部筛选叠加到日志自身的分组归属上。
func usageWhere(f Filter) (string, []any) {
	conditions := []string{"ul.group_id = ANY($1::bigint[])", "ul.created_at >= $2", "ul.created_at < $3"}
	args := []any{pq.Array(f.Groups), f.Start, f.End}
	for _, filter := range []struct {
		column string // 固定的筛选列。
		value  int64  // 筛选资源标识。
	}{{"ul.user_id", f.UserID}, {"ul.api_key_id", f.APIKeyID}, {"ul.account_id", f.AccountID}} {
		if filter.value > 0 {
			args = append(args, filter.value)
			conditions = append(conditions, fmt.Sprintf("%s = $%d", filter.column, len(args)))
		}
	}
	if f.Model != "" {
		args = append(args, f.Model)
		conditions = append(conditions, fmt.Sprintf("ul.model = $%d", len(args)))
	}
	if f.RequestType != "" {
		args = append(args, f.RequestType)
		conditions = append(conditions, fmt.Sprintf("%s = $%d", requestType, len(args)))
	}
	if f.Search != "" {
		args = append(args, "%"+f.Search+"%")
		conditions = append(conditions, fmt.Sprintf(`EXISTS (SELECT 1 FROM users u WHERE u.id = ul.user_id AND u.deleted_at IS NULL
            AND (u.username ILIKE $%d OR u.email ILIKE $%d))`, len(args), len(args)))
	}
	return " WHERE " + strings.Join(conditions, " AND "), args
}

// readJSON 只读取查询中显式构造的白名单对象。
func (s *Store) readJSON(ctx context.Context, query string, args ...any) (items []json.RawMessage, err error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("group view query: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	items = make([]json.RawMessage, 0)
	for rows.Next() {
		var item json.RawMessage
		if err := rows.Scan(&item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// page 执行有界列表与对应总数查询，排序表达式仅来自代码白名单。
func (s *Store) page(ctx context.Context, f Filter, from, projection, order, stable string, args []any) ([]json.RawMessage, int64, error) {
	var total int64
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM (SELECT 1 "+from+") counted", args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	direction := " ASC"
	if f.Desc {
		direction = " DESC"
	}
	args = append(args, f.PageSize, (f.Page-1)*f.PageSize)
	items, err := s.readJSON(ctx, fmt.Sprintf("SELECT %s %s ORDER BY %s%s NULLS LAST, %s LIMIT $%d OFFSET $%d", projection, from, order, direction, stable, len(args)-1, len(args)), args...)
	return items, total, err
}

// Accounts 按当前组绑定筛选账号，标签仅投影所选授权组，额度仅来自已有快照。
func (s *Store) Accounts(ctx context.Context, f Filter) (items []json.RawMessage, total int64, err error) {
	args := []any{pq.Array(f.Groups)}
	conditions := []string{"a.deleted_at IS NULL", `EXISTS (SELECT 1 FROM account_groups ag WHERE ag.account_id = a.id AND ag.group_id = ANY($1::bigint[]))`}
	for _, filter := range []struct {
		column string // 固定的账号筛选列。
		value  string // 对应筛选值。
	}{{"a.platform", f.Platform}, {"a.type", f.Type}, {"a.status", f.Status}} {
		if filter.value != "" {
			args = append(args, filter.value)
			conditions = append(conditions, fmt.Sprintf("%s = $%d", filter.column, len(args)))
		}
	}
	if f.Search != "" {
		args = append(args, "%"+f.Search+"%")
		conditions = append(conditions, fmt.Sprintf("a.name ILIKE $%d", len(args)))
	}
	order := map[string]string{"id": "a.id", "name": "a.name", "platform": "a.platform", "type": "a.type", "status": "a.status"}[f.Sort]
	if order == "" {
		order = "a.id"
	}
	from := " FROM accounts a WHERE " + strings.Join(conditions, " AND ")
	if err := s.db.QueryRowContext(ctx, "SELECT count(*)"+from, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	direction := " ASC"
	if f.Desc {
		direction = " DESC"
	}
	// 调度判断仅取所需标量和站内配额，绝不读取凭据或输出内部配额。
	projection := `a.id, a.name, a.platform, a.type, a.status, a.schedulable,
        a.auto_pause_on_expired, a.expires_at, a.rate_limit_reset_at, a.overload_until, a.temp_unschedulable_until,
        COALESCE(a.temp_unschedulable_reason, ''),
        jsonb_build_object('quota_limit',a.extra->'quota_limit','quota_used',a.extra->'quota_used',
            'quota_daily_limit',a.extra->'quota_daily_limit','quota_daily_used',a.extra->'quota_daily_used',
            'quota_daily_start',a.extra->'quota_daily_start','quota_daily_reset_mode',a.extra->'quota_daily_reset_mode',
            'quota_daily_reset_hour',a.extra->'quota_daily_reset_hour','quota_weekly_limit',a.extra->'quota_weekly_limit',
            'quota_weekly_used',a.extra->'quota_weekly_used','quota_weekly_start',a.extra->'quota_weekly_start',
            'quota_weekly_reset_mode',a.extra->'quota_weekly_reset_mode','quota_weekly_reset_day',a.extra->'quota_weekly_reset_day',
            'quota_weekly_reset_hour',a.extra->'quota_weekly_reset_hour','quota_reset_timezone',a.extra->'quota_reset_timezone'),
        (SELECT jsonb_agg(jsonb_build_object('id', g.id, 'name', g.name) ORDER BY g.id)
            FROM account_groups ag JOIN groups g ON g.id = ag.group_id
            WHERE ag.account_id = a.id AND g.id = ANY($1::bigint[]) AND g.deleted_at IS NULL), ` + quotaProjection() + `, ` + resetCreditsProjection()
	args = append(args, f.PageSize, (f.Page-1)*f.PageSize)
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf("SELECT %s%s ORDER BY %s%s, a.id LIMIT $%d OFFSET $%d", projection, from, order, direction, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	items = make([]json.RawMessage, 0)
	for rows.Next() {
		var a service.Account
		var extra, groups, quota, resetCredits []byte
		if err := rows.Scan(&a.ID, &a.Name, &a.Platform, &a.Type, &a.Status, &a.Schedulable, &a.AutoPauseOnExpired, &a.ExpiresAt,
			&a.RateLimitResetAt, &a.OverloadUntil, &a.TempUnschedulableUntil, &a.TempUnschedulableReason, &extra, &groups, &quota, &resetCredits); err != nil {
			return nil, 0, err
		}
		if err := json.Unmarshal(extra, &a.Extra); err != nil {
			return nil, 0, err
		}
		state, until := accountSchedulingState(&a, time.Now())
		item, err := json.Marshal(map[string]any{"id": a.ID, "name": a.Name, "platform": a.Platform, "type": a.Type, "status": a.Status,
			"schedulable": a.IsSchedulable(), "scheduling_enabled": a.Schedulable, "scheduling_state": state, "scheduling_until": until,
			"groups": json.RawMessage(groups), "quota": json.RawMessage(quota), "reset_credits": json.RawMessage(resetCredits)})
		if err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	return items, total, rows.Err()
}

// accountSchedulingState 将账号内部停调原因映射为可公开的固定状态及限制截止时间。
func accountSchedulingState(account *service.Account, now time.Time) (string, *time.Time) {
	if !account.IsActive() {
		return account.Status, nil
	}
	if !account.Schedulable {
		return "disabled", nil
	}
	if account.AutoPauseOnExpired && account.ExpiresAt != nil && !account.ExpiresAt.After(now) {
		return "expired", account.ExpiresAt
	}
	if account.OverloadUntil != nil && account.OverloadUntil.After(now) {
		return "overloaded", account.OverloadUntil
	}
	if account.RateLimitResetAt != nil && account.RateLimitResetAt.After(now) {
		return "rate_limited", account.RateLimitResetAt
	}
	if account.TempUnschedulableUntil != nil && account.TempUnschedulableUntil.After(now) {
		if service.IsAccountSchedulingThresholdReason(account.TempUnschedulableReason) {
			return "quota_paused", account.TempUnschedulableUntil
		}
		return "temp_unschedulable", account.TempUnschedulableUntil
	}
	if account.SupportsAccountQuota() && account.IsQuotaExceeded() {
		return "quota_exceeded", nil
	}
	return "available", nil
}

// People 仅聚合所选时段内实际产生授权组用量的人员。
func (s *Store) People(ctx context.Context, f Filter) ([]json.RawMessage, int64, error) {
	where, args := usageWhere(f)
	from := `FROM usage_logs ul LEFT JOIN users u ON u.id = ul.user_id AND u.deleted_at IS NULL` + where + ` GROUP BY ul.user_id, u.username, u.email`
	projection := `jsonb_build_object('id', ul.user_id, 'name', COALESCE(NULLIF(u.username, ''), 'User #' || ul.user_id),
        'email', COALESCE(u.email, ''), 'requests', count(*), 'tokens', COALESCE(sum(` + tokenSum + `),0),
        'cost', COALESCE(sum(ul.actual_cost),0), 'last_used_at', max(ul.created_at))`
	order := map[string]string{"name": "COALESCE(u.username,'')", "requests": "count(*)", "tokens": "sum(" + tokenSum + ")", "cost": "sum(ul.actual_cost)", "last_used_at": "max(ul.created_at)", "id": "ul.user_id"}[f.Sort]
	if order == "" {
		order = "sum(ul.actual_cost)"
	}
	return s.page(ctx, f, from, projection, order, "ul.user_id", args)
}

// Usage 读取日志安全字段，软删除或离组资源只保留历史标识。
func (s *Store) Usage(ctx context.Context, f Filter) ([]json.RawMessage, int64, error) {
	where, args := usageWhere(f)
	from := `FROM usage_logs ul
        LEFT JOIN users u ON u.id = ul.user_id AND u.deleted_at IS NULL
        LEFT JOIN api_keys k ON k.id = ul.api_key_id AND k.deleted_at IS NULL
        LEFT JOIN groups g ON g.id = ul.group_id AND g.deleted_at IS NULL
        LEFT JOIN accounts a ON a.id = ul.account_id AND a.deleted_at IS NULL
            AND EXISTS (SELECT 1 FROM account_groups ag WHERE ag.account_id = a.id AND ag.group_id = ANY($1::bigint[]))` + where
	projection := `jsonb_build_object('id', ul.id, 'created_at', ul.created_at,
        'user_id', ul.user_id, 'user_name', COALESCE(NULLIF(u.username,''), 'User #' || ul.user_id),
        'api_key_id', ul.api_key_id, 'api_key_name', COALESCE(k.name, 'Key #' || ul.api_key_id),
        'group_id', ul.group_id, 'group_name', g.name,
        'account_id', ul.account_id, 'account_name', COALESCE(a.name, 'Account #' || ul.account_id),
        'model', ul.model, 'request_type', ` + requestType + `, 'tokens', ` + tokenSum + `,
        'cost', ul.actual_cost, 'duration_ms', ul.duration_ms)`
	order := map[string]string{"id": "ul.id", "created_at": "ul.created_at", "tokens": tokenSum, "cost": "ul.actual_cost", "duration_ms": "ul.duration_ms", "model": "ul.model"}[f.Sort]
	if order == "" {
		order = "ul.created_at"
	}
	return s.page(ctx, f, from, projection, order, "ul.id", args)
}

// Summary 用相同日志范围计算汇总与按用户时区划分的每日趋势。
func (s *Store) Summary(ctx context.Context, f Filter) (map[string]any, error) {
	where, args := usageWhere(f)
	totals, err := s.readJSON(ctx, `SELECT jsonb_build_object('requests',count(*),'tokens',COALESCE(sum(`+tokenSum+`),0),'cost',COALESCE(sum(ul.actual_cost),0)) FROM usage_logs ul`+where, args...)
	if err != nil {
		return nil, err
	}
	args = append(args, f.Timezone)
	day := fmt.Sprintf("to_char(ul.created_at AT TIME ZONE $%d, 'YYYY-MM-DD')", len(args))
	trend, err := s.readJSON(ctx, `SELECT jsonb_build_object('date',`+day+`, 'requests',count(*),
        'input_tokens',sum(ul.input_tokens), 'output_tokens',sum(ul.output_tokens),
        'cache_creation_tokens',sum(ul.cache_creation_tokens), 'cache_read_tokens',sum(ul.cache_read_tokens),
        'total_tokens',sum(`+tokenSum+`), 'actual_cost',sum(ul.actual_cost), 'cost',sum(ul.actual_cost))
        FROM usage_logs ul`+where+` GROUP BY `+day+` ORDER BY `+day, args...)
	if err != nil {
		return nil, err
	}
	return map[string]any{"totals": totals[0], "trend": trend}, nil
}

// Options 从当前授权日志范围生成分页候选，不查询全站资源列表。
func (s *Store) Options(ctx context.Context, f Filter, kind, search string) ([]json.RawMessage, int64, error) {
	where, args := usageWhere(f)
	id, label, join := "", "", ""
	switch kind {
	case "user":
		id, label, join = "ul.user_id::text", "COALESCE(NULLIF(r.username,''), 'User #' || ul.user_id) || COALESCE(' (' || r.email || ')','')", " LEFT JOIN users r ON r.id = ul.user_id AND r.deleted_at IS NULL"
	case "api_key":
		id, label, join = "ul.api_key_id::text", "COALESCE(r.name, 'Key #' || ul.api_key_id)", " LEFT JOIN api_keys r ON r.id = ul.api_key_id AND r.deleted_at IS NULL"
	case "account":
		id, label, join = "ul.account_id::text", "COALESCE(r.name, 'Account #' || ul.account_id)", ` LEFT JOIN accounts r ON r.id = ul.account_id AND r.deleted_at IS NULL AND EXISTS (SELECT 1 FROM account_groups ag WHERE ag.account_id = r.id AND ag.group_id = ANY($1::bigint[]))`
	case "model":
		id, label = "ul.model", "ul.model"
	default:
		return nil, 0, fmt.Errorf("unsupported option kind")
	}
	from := "FROM (SELECT DISTINCT " + id + " AS id, " + label + " AS label FROM usage_logs ul" + join + where + ") options"
	if search != "" {
		args = append(args, "%"+search+"%")
		from += fmt.Sprintf(" WHERE label ILIKE $%d", len(args))
	}
	return s.page(ctx, f, from, "jsonb_build_object('id',id,'label',label)", "label", "id", args)
}
