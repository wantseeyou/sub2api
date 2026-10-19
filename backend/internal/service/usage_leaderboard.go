package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
)

var usageLeaderboardLocation = time.FixedZone("Asia/Shanghai", 8*60*60)

// UsageLeaderboardItem 是普通用户可见的用量排行数据。
type UsageLeaderboardItem struct {
	DisplayName  string  `json:"display_name"`  // 昵称，未设置时使用邮箱
	Requests     int64   `json:"requests"`      // 请求总数
	InputTokens  int64   `json:"input_tokens"`  // 输入 Token 总数
	OutputTokens int64   `json:"output_tokens"` // 输出 Token 总数
	CacheTokens  int64   `json:"cache_tokens"`  // 缓存创建和读取 Token 总数
	TotalTokens  int64   `json:"total_tokens"`  // 输入、输出和缓存 Token 总数
	ActualCost   float64 `json:"actual_cost"`   // 实际消费金额
}

// UsageLeaderboard 是按北京时间统计的用户用量排行榜。
type UsageLeaderboard struct {
	Users     []UsageLeaderboardItem `json:"users"`      // 排序后的用户用量
	StartDate string                 `json:"start_date"` // 包含在统计范围内的开始日期
	EndDate   string                 `json:"end_date"`   // 包含在统计范围内的结束日期
}

// IsUsageLeaderboardEnabled 读取排行榜开关，未配置时默认关闭。
func (s *SettingService) IsUsageLeaderboardEnabled(ctx context.Context) (bool, error) {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyUsageLeaderboardEnabled)
	if errors.Is(err, ErrSettingNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get usage leaderboard setting: %w", err)
	}
	return value == "true", nil
}

// usageLeaderboardRange 将日期预设转换为北京时间的左闭右开自然日范围。
func usageLeaderboardRange(period string, now time.Time) (time.Time, time.Time, error) {
	now = now.In(usageLeaderboardLocation)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, usageLeaderboardLocation)
	switch period {
	case "today":
		return today, today.AddDate(0, 0, 1), nil
	case "yesterday":
		return today.AddDate(0, 0, -1), today, nil
	case "7d":
		return today.AddDate(0, 0, -6), today.AddDate(0, 0, 1), nil
	case "30d":
		return today.AddDate(0, 0, -29), today.AddDate(0, 0, 1), nil
	default:
		return time.Time{}, time.Time{}, infraerrors.BadRequest("INVALID_LEADERBOARD_PERIOD", "Invalid period, use today/yesterday/7d/30d")
	}
}

// GetLeaderboard 汇总全部匹配记录后截取普通用户排行，不返回管理端成本字段。
func (s *UsageService) GetLeaderboard(ctx context.Context, period, sortBy string, limit int) (*UsageLeaderboard, error) {
	start, end, err := usageLeaderboardRange(period, time.Now())
	if err != nil {
		return nil, err
	}
	if limit != 50 && limit != 100 {
		return nil, infraerrors.BadRequest("INVALID_LEADERBOARD_LIMIT", "Invalid limit, use 50 or 100")
	}
	switch sortBy {
	case "requests", "input_tokens", "output_tokens", "cache_tokens", "total_tokens", "actual_cost":
	default:
		return nil, infraerrors.BadRequest("INVALID_LEADERBOARD_SORT", "Invalid leaderboard sort field")
	}
	stats, err := s.usageRepo.GetUserBreakdownStats(ctx, start, end, usagestats.UserBreakdownDimension{
		UserRole: RoleUser,
		SortBy:   sortBy,
	}, limit)
	if err != nil {
		return nil, fmt.Errorf("get usage leaderboard: %w", err)
	}
	result := &UsageLeaderboard{
		Users:     make([]UsageLeaderboardItem, 0, len(stats)),
		StartDate: start.Format(time.DateOnly),
		EndDate:   end.AddDate(0, 0, -1).Format(time.DateOnly),
	}
	for _, row := range stats {
		name := strings.TrimSpace(row.Username)
		if name == "" {
			name = row.Email
		}
		result.Users = append(result.Users, UsageLeaderboardItem{
			DisplayName:  name,
			Requests:     row.Requests,
			InputTokens:  row.InputTokens,
			OutputTokens: row.OutputTokens,
			CacheTokens:  row.CacheTokens,
			TotalTokens:  row.TotalTokens,
			ActualCost:   row.ActualCost,
		})
	}
	return result, nil
}
