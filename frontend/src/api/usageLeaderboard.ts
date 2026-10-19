import { apiClient } from './client'

export type UsageLeaderboardPeriod = 'today' | 'yesterday' | '7d' | '30d'
export type UsageLeaderboardSort = 'requests' | 'input_tokens' | 'output_tokens' | 'cache_tokens' | 'total_tokens' | 'actual_cost'

export interface UsageLeaderboardItem {
  display_name: string // 昵称，未设置时使用邮箱
  requests: number // 请求总数
  input_tokens: number // 输入 Token 总数
  output_tokens: number // 输出 Token 总数
  cache_tokens: number // 缓存创建和读取 Token 总数
  total_tokens: number // 输入、输出和缓存 Token 总数
  actual_cost: number // 实际消费金额
}

export interface UsageLeaderboardResponse {
  users: UsageLeaderboardItem[] // 排序后的用户用量
  start_date: string // 北京时间开始日期（含）
  end_date: string // 北京时间结束日期（含）
}

// 获取指定日期范围和排序方式的普通用户用量排行榜。
export async function getUsageLeaderboard(
  params: { period: UsageLeaderboardPeriod; sort_by: UsageLeaderboardSort; limit: 50 | 100 },
  signal?: AbortSignal
): Promise<UsageLeaderboardResponse> {
  const { data } = await apiClient.get<UsageLeaderboardResponse>('/usage/leaderboard', { params, signal })
  return data
}
