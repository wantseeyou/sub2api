import type { Account } from '@/types'

// 判断账号是否属于支持 Codex 高级选项和独立检测的普通 OpenAI 账号。
export function supportsCodexDiagnostics(account: Account | null | undefined): boolean {
  if (!account || account.platform !== 'openai' || !['oauth', 'setup-token'].includes(account.type)) return false
  return ![account.credentials?.auth_mode, account.credentials?.openai_auth_mode].some((value) => {
    const mode = typeof value === 'string' ? value.trim().toLowerCase().replace(/_/g, '') : ''
    return mode === 'agentidentity'
  })
}
