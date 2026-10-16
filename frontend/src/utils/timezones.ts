// 优先使用完整 IANA 时区目录，旧浏览器保留常用时区。
export function getTimezoneNames(): string[] {
  const intl = Intl as typeof Intl & { supportedValuesOf?: (key: 'timeZone') => string[] }
  return intl.supportedValuesOf?.('timeZone') ?? [
    'Asia/Shanghai', 'Asia/Tokyo', 'Asia/Singapore', 'Europe/London',
    'Europe/Berlin', 'America/New_York', 'America/Chicago', 'America/Denver',
    'America/Los_Angeles', 'America/Anchorage', 'Pacific/Honolulu', 'Australia/Sydney',
  ]
}
