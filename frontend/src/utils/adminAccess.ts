import type { User } from '@/types'

// adminRoutePermissions 显式声明后台页面需要的权限。
export const adminRoutePermissions: Record<string, string> = {
  '/admin/dashboard': 'dashboard.read',
  '/admin/ops': 'ops.read',
  '/admin/audit-logs': 'audit_logs.sensitive.read',
  '/admin/users': 'users.read',
  '/admin/groups': 'groups.read',
  '/admin/channels/pricing': 'channels.read',
  '/admin/channels/monitor': 'channel_monitors.read',
  '/admin/subscriptions': 'subscriptions.read',
  '/admin/accounts': 'accounts.read',
  '/admin/plugins': 'plugins.read',
  '/admin/announcements': 'announcements.read',
  '/admin/proxies': 'proxies.read',
  '/admin/redeem': 'redeem_codes.read',
  '/admin/promo-codes': 'promo.read',
  '/admin/settings': 'settings.read',
  '/admin/risk-control': 'risk_control.read',
  '/admin/prompt-audit': 'prompt_audit.read',
  '/admin/usage': 'usage.read',
  '/admin/affiliates/invites': 'affiliates.read',
  '/admin/affiliates/rebates': 'affiliates.read',
  '/admin/affiliates/transfers': 'affiliates.read',
  '/admin/orders/dashboard': 'payment.read',
  '/admin/orders/plans': 'payment.plans.read',
  '/admin/orders': 'payment.read'
}

// hasAdminPermission 判断用户是否拥有明确登记的操作权限。
export function hasAdminPermission(user: User | null | undefined, permission: string): boolean {
  return user?.role === 'admin' || Boolean(user?.admin_role?.enabled && user.admin_role.permissions.includes(permission))
}

// canAccessAdminPath 判断后台路由的访问权限，角色管理仅限内置管理员。
export function canAccessAdminPath(user: User | null | undefined, path: string): boolean {
  if (user?.role === 'admin') return true
  const permission = adminRoutePermissions[path]
  const prefix = permission?.replace(/\.read$/, '') + '.'
  return Boolean(permission && user?.admin_role?.enabled && user.admin_role.permissions.some(item => item === permission || item.startsWith(prefix)))
}

// getAdminHomePath 返回用户第一个有权访问的后台页面。
export function getAdminHomePath(user: User | null | undefined): string {
  return Object.keys(adminRoutePermissions).find(path => canAccessAdminPath(user, path)) || '/dashboard'
}
