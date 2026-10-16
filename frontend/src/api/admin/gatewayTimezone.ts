import { apiClient } from '../client'

export interface GatewayTimezoneDetection {
  ip: string // 服务器公网出口 IP。
  country: string // 国家名称。
  country_code: string // 国家代码。
  region: string // 州或省名称。
  city: string // 城市名称。
  timezone: string // IP 对应的 IANA 时区。
  detected_at: string // 最近成功检测时间。
}

export interface GatewayTimezoneSettings {
  timezone: string // 当前设置时区或初始默认值。
  is_set: boolean // 是否显式保存过时区。
  detection: GatewayTimezoneDetection // 最近成功检测结果。
}

// 获取网关时区和最近检测结果。
export async function getGatewayTimezoneSettings(): Promise<GatewayTimezoneSettings> {
  const { data } = await apiClient.get<GatewayTimezoneSettings>('/admin/settings/gateway-timezone')
  return data
}

// 独立保存手动选择的时区。
export async function saveGatewayTimezone(timezone: string): Promise<GatewayTimezoneSettings> {
  const { data } = await apiClient.put<GatewayTimezoneSettings>('/admin/settings/gateway-timezone', { timezone })
  return data
}

// 刷新服务器出口检测，并按需将检测时区设为当前配置。
export async function detectGatewayTimezone(apply: boolean): Promise<GatewayTimezoneSettings> {
  const { data } = await apiClient.post<GatewayTimezoneSettings>('/admin/settings/gateway-timezone/detect', { apply })
  return data
}
