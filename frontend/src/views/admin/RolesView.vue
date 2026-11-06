<template>
  <AppLayout>
    <div class="space-y-5">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div><h1 class="text-xl font-semibold">{{ t('roles.title') }}</h1><p class="mt-1 text-sm text-gray-500">{{ t('roles.description') }}</p></div>
        <button class="btn btn-primary" @click="openEditor()">{{ t('roles.create') }}</button>
      </div>
      <div class="grid gap-3 sm:grid-cols-2">
        <div v-for="role in builtInRoles" :key="role.name" class="rounded-xl border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-800">
          <span class="font-medium">{{ role.name }}</span><span class="badge badge-gray ml-2">{{ t('roles.builtIn') }}</span><p class="mt-2 text-sm text-gray-500">{{ role.description }}</p>
        </div>
      </div>
      <form class="flex gap-2" @submit.prevent="page = 1; load()">
        <input v-model="search" type="search" class="input sm:max-w-xs" :placeholder="t('roles.search')" :aria-label="t('roles.search')" />
        <button type="submit" class="btn btn-secondary">{{ t('common.search') }}</button>
      </form>
      <div v-if="error" role="alert" class="rounded-lg bg-red-50 p-3 text-sm text-red-700 dark:bg-red-900/20">{{ error }} <button class="underline" @click="loadAll">{{ t('common.tryAgain') }}</button></div>
      <div class="overflow-x-auto rounded-xl border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-800" :aria-busy="loading">
        <table class="min-w-full text-left text-sm">
          <thead class="bg-gray-50 text-gray-500 dark:bg-dark-700"><tr><th class="p-4">{{ t('roles.name') }}</th><th class="p-4">{{ t('common.status') }}</th><th class="p-4">{{ t('roles.users') }}</th><th class="p-4">{{ t('roles.permissions') }}</th><th class="p-4">{{ t('common.actions') }}</th></tr></thead>
          <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
            <tr v-for="role in roles" :key="role.id"><td class="p-4"><div class="font-medium">{{ role.name }}</div><p class="mt-1 max-w-sm text-gray-500">{{ role.description }}</p></td><td class="p-4"><span class="badge" :class="role.enabled ? 'badge-success' : 'badge-gray'">{{ t(role.enabled ? 'roles.enabled' : 'roles.disabled') }}</span></td><td class="p-4">{{ role.user_count }}</td><td class="p-4">{{ role.permissions.length }}</td><td class="p-4"><div class="flex flex-wrap gap-2"><button class="btn btn-secondary btn-sm" @click="openEditor(role)">{{ t('common.edit') }}</button><button class="btn btn-secondary btn-sm" @click="openEditor(role, true)">{{ t('roles.copy') }}</button><button class="btn btn-secondary btn-sm" :disabled="busyId === role.id" @click="toggleRole(role)">{{ t(role.enabled ? 'roles.disable' : 'roles.enable') }}</button><button class="btn btn-danger btn-sm" :disabled="role.user_count > 0 || busyId === role.id" :title="role.user_count > 0 ? t('roles.assignedCannotDelete') : undefined" @click="deleting = role">{{ t('common.delete') }}</button></div></td></tr>
            <tr v-if="!roles.length"><td colspan="5" class="p-8 text-center text-gray-500">{{ loading ? t('common.loading') : t('empty.noData') }}</td></tr>
          </tbody>
        </table>
        <Pagination v-if="total > 0" :page="page" :total="total" :page-size="pageSize" @update:page="changePage" @update:pageSize="changePageSize" />
      </div>
    </div>
    <BaseDialog :show="editing" :title="editingId ? t('roles.edit') : t('roles.create')" width="wide" @close="editing = false">
      <form id="role-form" class="space-y-4" @submit.prevent="save">
        <label class="block"><span class="input-label">{{ t('roles.name') }}</span><input v-model="form.name" required maxlength="64" class="input" /></label>
        <label class="block"><span class="input-label">{{ t('roles.roleDescription') }}</span><textarea v-model="form.description" rows="2" maxlength="500" class="input" /></label>
        <label class="flex items-center gap-2 text-sm"><input v-model="form.enabled" type="checkbox" />{{ t('roles.enabled') }}</label>
        <p v-if="editorError" role="alert" class="text-sm text-red-600">{{ editorError }}</p>
        <PermissionTree v-model="form.permissions" :permissions="permissions" />
      </form>
      <template #footer><div class="flex justify-end gap-2"><button class="btn btn-secondary" @click="editing = false">{{ t('common.cancel') }}</button><button type="submit" form="role-form" class="btn btn-primary" :disabled="saving || !permissions.length">{{ saving ? t('common.saving') : t('common.save') }}</button></div></template>
    </BaseDialog>
    <BaseDialog :show="!!deleting" :title="t('roles.deleteTitle')" width="normal" @close="deleting = null">
      <p>{{ t('roles.deleteConfirm', { name: deleting?.name }) }}</p>
      <template #footer><div class="flex justify-end gap-2"><button class="btn btn-secondary" @click="deleting = null">{{ t('common.cancel') }}</button><button class="btn btn-danger" :disabled="busyId !== null" @click="deleteRole">{{ t('common.delete') }}</button></div></template>
    </BaseDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Pagination from '@/components/common/Pagination.vue'
import PermissionTree from '@/components/admin/PermissionTree.vue'
import rolesAPI, { type AdminRoleInput } from '@/api/admin/roles'
import { useAppStore } from '@/stores/app'
import type { AdminRole, AdminPermission } from '@/types'

const { t } = useI18n()
const app = useAppStore()
const roles = ref<AdminRole[]>([])
const permissions = ref<AdminPermission[]>([])
const search = ref('')
const page = ref(1)
const pageSize = ref(20)
const total = ref(0)
const loading = ref(false)
const error = ref('')
const busyId = ref<number | null>(null)
const editing = ref(false)
const editingId = ref<number | null>(null)
const deleting = ref<AdminRole | null>(null)
const saving = ref(false)
const editorError = ref('')
const form = reactive<AdminRoleInput>({ name: '', description: '', enabled: true, permissions: [] })
const builtInRoles = computed(() => [
  { name: t('admin.users.roles.user'), description: t('roles.userDescription') },
  { name: t('admin.users.roles.admin'), description: t('roles.adminDescription') }
])
let loadVersion = 0

// load 获取当前页角色并丢弃过期请求结果。
async function load() {
  const version = ++loadVersion
  loading.value = true
  error.value = ''
  try {
    const result = await rolesAPI.list(page.value, pageSize.value, search.value)
    if (version !== loadVersion) return
    roles.value = result.items
    total.value = result.total
  } catch (err) {
    if (version === loadVersion) error.value = err instanceof Error ? err.message : t('roles.loadFailed')
  } finally {
    if (version === loadVersion) loading.value = false
  }
}

// openEditor 打开新增、编辑或复制角色表单。
function openEditor(role?: AdminRole, copy = false) {
  editingId.value = role && !copy ? role.id : null
  Object.assign(form, { name: role ? `${role.name}${copy ? t('roles.copySuffix') : ''}` : '', description: role?.description || '', enabled: role?.enabled ?? true, permissions: [...(role?.permissions || [])] })
  editorError.value = ''
  editing.value = true
}

// save 保存角色及其明确选择的权限集合。
async function save() {
  if (saving.value || !form.name.trim()) return
  saving.value = true
  editorError.value = ''
  try {
    const input = { ...form, name: form.name.trim(), permissions: [...form.permissions] }
    if (editingId.value) await rolesAPI.update(editingId.value, input)
    else await rolesAPI.create(input)
    editing.value = false
    app.showSuccess(t('roles.saved'))
    await load()
  } catch (err) {
    editorError.value = err instanceof Error ? err.message : t('roles.saveFailed')
  } finally {
    saving.value = false
  }
}

// toggleRole 切换后台角色启用状态。
async function toggleRole(role: AdminRole) {
  busyId.value = role.id
  try {
    await rolesAPI.update(role.id, { name: role.name, description: role.description, enabled: !role.enabled, permissions: role.permissions })
    await load()
  } catch (err) {
    app.showError(err instanceof Error ? err.message : t('roles.saveFailed'))
  } finally { busyId.value = null }
}

// deleteRole 删除没有已分配用户的角色。
async function deleteRole() {
  if (!deleting.value || deleting.value.user_count > 0) return
  busyId.value = deleting.value.id
  try {
    await rolesAPI.remove(deleting.value.id)
    deleting.value = null
    if (roles.value.length === 1 && page.value > 1) page.value--
    await load()
  } catch (err) {
    app.showError(err instanceof Error ? err.message : t('roles.deleteFailed'))
  } finally { busyId.value = null }
}

// changePage 切换角色列表页码。
function changePage(value: number) { page.value = value; void load() }
// changePageSize 调整角色列表每页数量。
function changePageSize(value: number) { pageSize.value = value; page.value = 1; void load() }

// loadAll 加载角色列表和权限目录，支持失败后重试。
async function loadAll() {
  await Promise.all([load(), rolesAPI.permissions().then(result => { permissions.value = result }).catch(err => { error.value = err instanceof Error ? err.message : t('roles.loadFailed') })])
}
onMounted(loadAll)
</script>
