import { apiClient } from '../client'
import type { AdminRole, AdminPermission, PaginatedResponse } from '@/types'

export type AdminRoleInput = Pick<AdminRole, 'name' | 'description' | 'enabled' | 'permissions'>

// list 获取后台角色的分页列表。
export async function list(page = 1, pageSize = 20, search = ''): Promise<PaginatedResponse<AdminRole>> {
  const { data } = await apiClient.get('/admin/roles', { params: { page, page_size: pageSize, search } })
  return data
}

// permissions 获取可配置的权限目录。
export async function permissions(): Promise<AdminPermission[]> {
  const { data } = await apiClient.get('/admin/roles/permissions')
  return data
}

// create 创建自定义后台角色。
export async function create(input: AdminRoleInput): Promise<AdminRole> {
  const { data } = await apiClient.post('/admin/roles', input)
  return data
}

// update 更新自定义后台角色。
export async function update(id: number, input: AdminRoleInput): Promise<AdminRole> {
  const { data } = await apiClient.put(`/admin/roles/${id}`, input)
  return data
}

// remove 删除未分配用户的后台角色。
export async function remove(id: number): Promise<void> {
  await apiClient.delete(`/admin/roles/${id}`)
}

export default { list, permissions, create, update, remove }
