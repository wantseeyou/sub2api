import { apiClient } from './client'
import type { PaginatedResponse, TrendDataPoint } from '@/types'

export interface ViewGroup {
  id: number // 分组标识。
  name: string // 分组名称。
}

export interface QuotaWindow {
  name: string // 共享额度窗口名称。
  used_percent: number // 上游已用百分比。
  resets_at?: string | number | null // 上游窗口重置时间。
  updated_at?: string | null // 快照采样时间。
}

export interface ViewResetCredits {
  available_count: number | null // 缓存中的可用重置卡次数，缺失时未知。
  credits: { expires_at: string }[] // 仅保留卡片到期时间。
}

export interface ViewRow {
  id: number // 当前行标识。
  name?: string // 账号或人员名称。
  email?: string // 人员识别邮箱。
  platform?: string // 上游平台。
  type?: string // 上游账号类型。
  status?: string // 上游账号状态。
  schedulable?: boolean // 当前可调度状态。
  scheduling_enabled?: boolean // 管理员配置的调度开关。
  scheduling_state?: string // 脱敏后的实际调度状态。
  scheduling_until?: string | null // 当前调度限制的截止时间。
  reset_credits?: ViewResetCredits | null // 仅供展示的重置卡缓存。
  groups?: ViewGroup[] // 本次所选的可见分组。
  quota?: QuotaWindow[] // 共享上游额度快照。
  requests?: number // 组内请求数。
  tokens?: number // 组内 Token 数。
  cost?: number // 对用户的计费金额。
  last_used_at?: string // 最近使用时间。
  created_at?: string // 调用发生时间。
  user_id?: number // 历史人员标识。
  user_name?: string // 历史人员显示名称。
  api_key_id?: number // 历史密钥标识。
  api_key_name?: string // 历史密钥显示名称。
  account_id?: number // 历史账号标识。
  account_name?: string // 历史账号安全名称。
  group_id?: number // 日志所属分组。
  group_name?: string // 日志所属分组名称。
  model?: string // 日志模型名称。
  request_type?: string // 请求类型。
  duration_ms?: number // 请求耗时。
}

export interface ViewOption {
  id: string // 筛选值。
  label: string // 安全显示名称。
}

export interface ViewSummary {
  totals: { requests: number; tokens: number; cost: number } // 所选范围汇总。
  trend: TrendDataPoint[] // 所选范围的每日趋势。
}

export type ViewParams = Record<string, string | number | undefined>
export type ViewTab = 'accounts' | 'people' | 'usage'
export type OptionKind = 'user' | 'api_key' | 'account' | 'model'

// getAccess 读取用于界面展示的最新分组授权。
export async function getAccess(signal?: AbortSignal): Promise<ViewGroup[]> {
  const { data } = await apiClient.get<ViewGroup[]>('/group-view/access', { signal })
  return data
}

// listView 读取具有明确分组边界的分页数据。
export async function listView(tab: ViewTab, params: ViewParams, signal?: AbortSignal): Promise<PaginatedResponse<ViewRow>> {
  const { data } = await apiClient.get<PaginatedResponse<ViewRow>>(`/group-view/${tab}`, { params, signal })
  return data
}

// getSummary 读取与列表条件一致的组内汇总和趋势。
export async function getSummary(params: ViewParams, signal?: AbortSignal): Promise<ViewSummary> {
  const { data } = await apiClient.get<ViewSummary>('/group-view/summary', { params, signal })
  return data
}

// getOptions 只查询当前范围内出现过的分页候选。
export async function getOptions(params: ViewParams, signal?: AbortSignal): Promise<PaginatedResponse<ViewOption>> {
  const { data } = await apiClient.get<PaginatedResponse<ViewOption>>('/group-view/options', { params, signal })
  return data
}

// getUserGrants 供管理员读取用户的查看授权。
export async function getUserGrants(userID: number): Promise<ViewGroup[]> {
  const { data } = await apiClient.get<ViewGroup[]>(`/admin/users/${userID}/group-view-grants`)
  return data
}

// setUserGrants 供管理员提交完整的查看授权集合。
export async function setUserGrants(userID: number, groupIDs: number[]): Promise<void> {
  await apiClient.put(`/admin/users/${userID}/group-view-grants`, { group_ids: groupIDs })
}
