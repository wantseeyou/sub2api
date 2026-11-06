// accountEditPermissions 列出通用账号表单可编辑的权限域。
export const accountEditPermissions = [
  'accounts.update', 'accounts.status', 'accounts.schedule', 'accounts.network',
  'accounts.groups', 'accounts.credentials', 'accounts.models.write',
  'accounts.configuration', 'accounts.test'
]

const modelKeys = new Set(['model_mapping', 'compact_model_mapping', 'model_whitelist', 'model_restriction', 'allowed_models'])
const networkKeys = new Set(['base_url', 'api_base_urls', 'proxy_fallback', 'proxy_fallback_url', 'header_override_enabled', 'header_overrides', 'protocol_rules'])
const credentialKeys = new Set([
  'access_token', 'refresh_token', 'id_token', 'agent_private_key', 'api_key', 'session_key', 'cookie',
  'password', 'sso_token', 'sso', 'sso-rw', 'clearTextPassword', 'aws_secret_access_key',
  'aws_session_token', 'service_account_json', 'service_account', 'private_key', 'client_secret', '_token_version'
])
const deletableCredentialKeys = new Set([
  ...modelKeys, ...networkKeys, 'openai_capabilities', 'zhipu_organization', 'zhipu_project',
  'pool_mode', 'pool_mode_retry_count', 'pool_mode_retry_status_codes', 'custom_error_codes_enabled',
  'custom_error_codes', 'project_id', 'aws_force_global', 'temp_unschedulable_enabled',
  'temp_unschedulable_rules', 'intercept_warmup_requests', 'antigravity_project_id', 'plan_type',
  'explicit_cache_upstream', 'explicit_cache_upstream_max_ttl_seconds', 'account_scheduling_threshold'
])
const deletableExtraKeys = new Set([
  'mixed_scheduling', 'allow_overages', 'window_cost_limit', 'window_cost_sticky_reserve',
  'max_sessions', 'session_idle_timeout_minutes', 'rpm_sticky_buffer', 'base_rpm', 'rpm_strategy',
  'user_msg_queue_mode', 'user_msg_queue_enabled', 'tls_fingerprint_profile_id', 'enable_tls_fingerprint',
  'session_id_masking_enabled', 'cache_ttl_override_enabled', 'cache_ttl_override_target',
  'custom_base_url_enabled', 'custom_base_url', 'anthropic_passthrough', 'anthropic_apikey_auth_scheme',
  'web_search_emulation', 'responses_websockets_v2_enabled', 'openai_ws_enabled', 'openai_passthrough',
  'openai_oauth_passthrough', 'openai_responses_flatten_namespaces', 'openai_long_context_billing_enabled',
  'openai_compact_mode', 'openai_responses_mode', 'images_url_to_b64_json', 'auto_pause_5h_threshold',
  'auto_pause_7d_threshold', 'auto_pause_5h_disabled', 'auto_pause_7d_disabled',
  'codex_image_generation_bridge_enabled', 'codex_image_generation_explicit_tool_policy',
  'codex_image_generation_bridge', 'codex_cli_only', 'codex_cli_only_allowed_clients',
  'codex_cli_only_allow_app_server', 'codex_fingerprint_mode', 'quota_limit', 'quota_daily_limit',
  'quota_daily_used', 'quota_daily_start', 'quota_weekly_limit', 'quota_weekly_used', 'quota_weekly_start',
  'quota_daily_reset_mode', 'quota_daily_reset_hour', 'quota_weekly_reset_mode', 'quota_weekly_reset_day',
  'quota_weekly_reset_hour', 'quota_reset_timezone', 'upstream_request_id_header',
  ...['daily', 'weekly', 'total'].flatMap(window => ['enabled', 'threshold', 'threshold_type'].map(field => `quota_notify_${window}_${field}`))
])

// accountCredentialPermission 与后端保持一致，区分认证信息、模型、网络和高级配置。
export function accountCredentialPermission(key: string): string {
  if (modelKeys.has(key)) return 'accounts.models.write'
  if (networkKeys.has(key)) return 'accounts.network'
  return credentialKeys.has(key) ? 'accounts.credentials' : 'accounts.configuration'
}

// accountExtraPermission 区分 extra 中的网络、探测和高级配置字段。
export function accountExtraPermission(key: string): string {
  if (key === 'custom_base_url' || key === 'custom_base_url_enabled') return 'accounts.network'
  if (key === 'upstream_billing_probe_enabled' || key === 'upstream_billing_rate_sync_enabled') return 'accounts.test'
  return 'accounts.configuration'
}

// sameValue 忽略对象键顺序，比较表单生成的 JSON 配置值。
function sameValue(left: unknown, right: unknown): boolean {
  if (Object.is(left, right)) return true
  if (!left || !right || typeof left !== 'object' || typeof right !== 'object') return false
  if (Array.isArray(left) !== Array.isArray(right)) return false
  const a = left as Record<string, unknown>
  const b = right as Record<string, unknown>
  const keys = Object.keys(a)
  return keys.length === Object.keys(b).length && keys.every(key => Object.prototype.hasOwnProperty.call(b, key) && sameValue(a[key], b[key]))
}

// buildAccountRolePatch 仅提交角色有权且实际改动的字段，使用 null 删除已存在的非认证配置。
export function buildAccountRolePatch(
  initial: Record<string, unknown>,
  current: Record<string, unknown>,
  can: (permission: string) => boolean
): Record<string, unknown> {
  const result: Record<string, unknown> = {}
  const fields: Record<string, string> = {
    name: 'accounts.update', notes: 'accounts.update', proxy_id: 'accounts.network', group_ids: 'accounts.groups',
    concurrency: 'accounts.schedule', priority: 'accounts.schedule', load_factor: 'accounts.schedule',
    rate_multiplier: 'accounts.schedule', expires_at: 'accounts.schedule', auto_pause_on_expired: 'accounts.schedule',
    upstream_billing_probe_enabled: 'accounts.test', upstream_billing_rate_sync_enabled: 'accounts.test',
    status: current.status === 'active' || current.status === 'inactive' ? 'accounts.status' : 'accounts.configuration'
  }
  for (const [key, permission] of Object.entries(fields)) {
    if (can(permission) && Object.prototype.hasOwnProperty.call(current, key) && !sameValue(initial[key], current[key])) result[key] = current[key]
  }
  for (const field of ['credentials', 'extra']) {
    const before = (initial[field] || {}) as Record<string, unknown>
    const after = (current[field] || {}) as Record<string, unknown>
    const patch: Record<string, unknown> = {}
    for (const key of new Set([...Object.keys(before), ...Object.keys(after)])) {
      if (field === 'extra' && (key === 'upstream_billing_probe_enabled' || key === 'upstream_billing_rate_sync_enabled')) continue
      const permission = field === 'credentials' ? accountCredentialPermission(key) : accountExtraPermission(key)
      if (!can(permission) || sameValue(before[key], after[key])) continue
      const removing = !Object.prototype.hasOwnProperty.call(after, key) || after[key] === null
      if (removing && field === 'credentials' && (!deletableCredentialKeys.has(key) || credentialKeys.has(key))) continue
      if (removing && field === 'extra' && !deletableExtraKeys.has(key)) continue
      patch[key] = removing ? null : after[key]
    }
    if (Object.keys(patch).length) result[field] = patch
  }
  return result
}
