import { describe, expect, it } from 'vitest'
import { buildAccountRolePatch } from '../accountEditPermissions'

describe('上游分组同步权限', () => {
  it.each([
    { permissions: ['accounts.update'], expected: {} },
    { permissions: ['accounts.test'], expected: {} },
    { permissions: ['accounts.update', 'accounts.test'], expected: { upstream_billing_group_sync_enabled: true } }
  ])('仅编辑和探测均获授权时提交同步设置：$permissions', ({ permissions, expected }) => {
    expect(buildAccountRolePatch(
      { upstream_billing_group_sync_enabled: false },
      { upstream_billing_group_sync_enabled: true },
      permission => permissions.includes(permission)
    )).toEqual(expected)
  })
})
