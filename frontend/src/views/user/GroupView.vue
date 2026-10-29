<template>
  <AppLayout>
    <div class="space-y-5">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 class="text-2xl font-bold text-gray-900 dark:text-white">{{ t('groupView.title') }}</h1>
          <p class="mt-1 text-sm text-gray-500">{{ t('groupView.description') }}</p>
        </div>
        <button class="btn btn-secondary" :disabled="loading" @click="refresh">{{ t('common.refresh') }}</button>
      </div>
      <p v-if="error" class="rounded-lg bg-red-50 p-3 text-sm text-red-600 dark:bg-red-900/20" role="alert">{{ error }}</p>
      <div v-if="groups.length" class="card space-y-4 p-4">
        <div class="grid gap-4 md:grid-cols-3">
          <label class="space-y-1 text-sm">
            <span>{{ t('groupView.groups') }}</span>
            <select v-model="selectedGroups" multiple class="input w-full" :aria-label="t('groupView.groups')">
              <option v-for="group in groups" :key="group.id" :value="group.id">{{ group.name }}</option>
            </select>
            <span class="text-xs text-gray-500">{{ t('groupView.allGroupsHint') }}</span>
          </label>
          <label class="space-y-1 text-sm">
            <span>{{ t('groupView.start') }}</span>
            <input v-model="startDate" type="date" class="input w-full" />
          </label>
          <label class="space-y-1 text-sm">
            <span>{{ t('groupView.end') }}</span>
            <input v-model="endDate" type="date" class="input w-full" />
          </label>
        </div>
        <div class="flex gap-2 border-b dark:border-dark-600" role="tablist" :aria-label="t('groupView.title')">
          <button v-for="item in tabs" :key="item" class="border-b-2 px-4 py-2 text-sm" :class="tab === item ? 'border-primary-500 text-primary-600' : 'border-transparent text-gray-500'" role="tab" :aria-selected="tab === item" @click="tab = item">{{ t(`groupView.${item}`) }}</button>
        </div>
        <div v-if="tab === 'accounts'" class="grid gap-3 sm:grid-cols-4">
          <label class="text-sm">{{ t('groupView.name') }}<input v-model="search" class="input mt-1 w-full" /></label>
          <label class="text-sm">{{ t('groupView.platform') }}<input v-model="platform" class="input mt-1 w-full" /></label>
          <label class="text-sm">{{ t('groupView.type') }}<input v-model="accountType" class="input mt-1 w-full" /></label>
          <label class="text-sm">{{ t('groupView.status') }}<select v-model="status" class="input mt-1 w-full"><option value="">{{ t('common.all') }}</option><option v-for="value in ['active', 'inactive', 'error']" :key="value" :value="value">{{ value }}</option></select></label>
        </div>
        <div v-else class="grid gap-3 sm:grid-cols-2 lg:grid-cols-5">
          <GroupViewResourceFilter v-model="userID" kind="user" :label="t('groupView.person')" :params="scopeParams" />
          <GroupViewResourceFilter v-model="model" kind="model" :label="t('groupView.model')" :params="scopeParams" />
          <template v-if="tab === 'usage'">
            <GroupViewResourceFilter v-model="apiKeyID" kind="api_key" :label="t('groupView.apiKey')" :params="scopeParams" />
            <GroupViewResourceFilter v-model="accountID" kind="account" :label="t('groupView.account')" :params="scopeParams" />
            <label class="text-sm">{{ t('groupView.requestType') }}<select v-model="requestType" class="input mt-1 w-full"><option value="">{{ t('common.all') }}</option><option v-for="value in ['sync', 'stream', 'ws_v2', 'cyber', 'live']" :key="value" :value="value">{{ value }}</option></select></label>
          </template>
        </div>
      </div>
      <template v-if="groups.length && tab !== 'accounts' && summary">
        <p class="text-xs text-gray-500">{{ t('groupView.groupUsageHint') }}</p>
        <div class="grid grid-cols-3 gap-3">
          <div v-for="key in ['requests', 'tokens', 'cost'] as const" :key="key" class="card p-4">
            <p class="text-sm text-gray-500">{{ t(`groupView.${key}`) }}</p>
            <p class="mt-1 text-xl font-semibold">{{ key === 'cost' ? money(summary.totals[key]) : summary.totals[key].toLocaleString() }}</p>
          </div>
        </div>
        <TokenUsageTrend :trend-data="summary.trend" :loading="loading" />
      </template>
      <div v-if="groups.length" class="card overflow-hidden">
        <div class="flex flex-wrap gap-3 border-b p-3 text-xs dark:border-dark-600">
          <span>{{ t('groupView.columns') }}</span>
          <label v-for="column in allColumns" :key="column.key" class="flex items-center gap-1">
            <input type="checkbox" :checked="!hiddenColumns.includes(column.key)" @change="toggleColumn(column.key)" />{{ column.label }}
          </label>
          <label class="ml-auto flex items-center gap-1">
            {{ t('pagination.perPage') }}:
            <select :value="pageSize" class="input py-1" @change="changePageSize(Number(($event.target as HTMLSelectElement).value))">
              <option v-for="size in pageSizeOptions" :key="size" :value="size">{{ size }}</option>
            </select>
          </label>
        </div>
        <DataTable :key="tab" :columns="columns" :data="rows" :loading="loading" server-side-sort :default-sort-key="sortBy" :default-sort-order="sortOrder" @sort="sort">
          <template #cell-status="{ value }">
            <span class="badge" :class="value === 'active' ? 'badge-success' : value === 'error' ? 'badge-danger' : 'badge-gray'">{{ t(`admin.accounts.status.${value}`) }}</span>
          </template>
          <template #cell-schedulable="{ row }">
            <div class="min-w-32 space-y-1">
              <span class="badge" :class="row.schedulable ? 'badge-success' : row.scheduling_state === 'disabled' || row.status === 'inactive' ? 'badge-gray' : 'badge-warning'">{{ t(`groupView.schedulingStates.${row.scheduling_state || (row.schedulable ? 'available' : 'disabled')}`) }}</span>
              <p class="text-[10px] text-gray-400">{{ row.scheduling_enabled ? t('groupView.schedulingEnabled') : t('groupView.schedulingDisabled') }}</p>
              <p v-if="row.scheduling_until" class="text-[10px] text-gray-400">{{ t('groupView.schedulingUntil') }}: {{ dateTime(row.scheduling_until) }}</p>
            </div>
          </template>
          <template #cell-groups="{ row }">{{ row.groups?.map((group: ViewGroup) => group.name).join(', ') }}</template>
          <template #cell-quota="{ row }">
            <GroupViewAccountUsage :account="row" />
          </template>
          <template #cell-cost="{ value }">{{ money(value) }}</template>
          <template #cell-created_at="{ value }">{{ dateTime(value) }}</template>
          <template #cell-last_used_at="{ value }">{{ dateTime(value) }}</template>
        </DataTable>
        <Pagination :page="page" :page-size="pageSize" :total="total" :show-page-size-selector="false" @update:page="page = $event" />
      </div>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import DataTable from '@/components/common/DataTable.vue'
import type { Column } from '@/components/common/types'
import Pagination from '@/components/common/Pagination.vue'
import TokenUsageTrend from '@/components/charts/TokenUsageTrend.vue'
import GroupViewResourceFilter from '@/components/user/GroupViewResourceFilter.vue'
import GroupViewAccountUsage from '@/components/user/GroupViewAccountUsage.vue'
import { getAccess, getSummary, listView, type ViewGroup, type ViewParams, type ViewRow, type ViewSummary, type ViewTab } from '@/api/groupView'
import { getConfiguredTablePageSizeOptions } from '@/utils/tablePreferences'

const { t } = useI18n()
const tabs: ViewTab[] = ['accounts', 'people', 'usage']
const tab = ref<ViewTab>('accounts')
const groups = ref<ViewGroup[]>([])
const selectedGroups = ref<number[]>([])
const today = new Date()
const weekAgo = new Date(today)
weekAgo.setDate(weekAgo.getDate() - 6)
// localDate 保留浏览器本地日历日期，避免 UTC 转换跨日。
function localDate(value: Date) { return `${value.getFullYear()}-${String(value.getMonth() + 1).padStart(2, '0')}-${String(value.getDate()).padStart(2, '0')}` }
const startDate = ref(localDate(weekAgo))
const endDate = ref(localDate(today))
const search = ref('')
const platform = ref('')
const accountType = ref('')
const status = ref('')
const userID = ref('')
const model = ref('')
const apiKeyID = ref('')
const accountID = ref('')
const requestType = ref('')
const page = ref(1)
const pageSize = ref(20)
const maxPageSize = 100
const pageSizeOptions = computed(() => Array.from(new Set([pageSize.value, ...getConfiguredTablePageSizeOptions().filter(size => size <= maxPageSize)])).sort((a, b) => a - b))
const sortBy = ref('id')
const sortOrder = ref<'asc' | 'desc'>('desc')
const rows = ref<ViewRow[]>([])
const summary = ref<ViewSummary | null>(null)
const total = ref(0)
const loading = ref(false)
const error = ref('')
const hiddenColumns = ref<string[]>([])
let controller: AbortController | undefined
let timer: ReturnType<typeof setTimeout> | undefined

const scopeParams = computed<ViewParams>(() => {
  const start = new Date(`${startDate.value}T00:00:00`)
  const end = new Date(`${endDate.value}T00:00:00`)
  end.setDate(end.getDate() + 1)
  return {
    group_ids: selectedGroups.value.length ? selectedGroups.value.join(',') : undefined,
    start_time: Number.isFinite(start.getTime()) ? start.toISOString() : '',
    end_time: Number.isFinite(end.getTime()) ? end.toISOString() : '',
    timezone: Intl.DateTimeFormat().resolvedOptions().timeZone
  }
})
const filters = computed<ViewParams>(() => tab.value === 'accounts' ? {
  group_ids: scopeParams.value.group_ids, search: search.value || undefined, platform: platform.value || undefined,
  type: accountType.value || undefined, status: status.value || undefined
} : {
  ...scopeParams.value, user_id: userID.value || undefined, model: model.value || undefined,
  api_key_id: tab.value === 'usage' ? apiKeyID.value || undefined : undefined,
  account_id: tab.value === 'usage' ? accountID.value || undefined : undefined,
  request_type: tab.value === 'usage' ? requestType.value || undefined : undefined
})

const columnKeys = computed(() => tab.value === 'accounts'
  ? ['id', 'name', 'platform', 'type', 'status', 'schedulable', 'groups', 'quota']
  : tab.value === 'people'
    ? ['id', 'name', 'email', 'requests', 'tokens', 'cost', 'last_used_at']
    : ['created_at', 'user_name', 'api_key_name', 'group_name', 'account_name', 'model', 'request_type', 'tokens', 'cost', 'duration_ms'])
const sortableKeys = computed(() => tab.value === 'accounts'
  ? ['id', 'name', 'platform', 'type', 'status']
  : tab.value === 'people' ? ['id', 'name', 'requests', 'tokens', 'cost', 'last_used_at']
    : ['created_at', 'model', 'tokens', 'cost', 'duration_ms'])
const allColumns = computed<Column[]>(() => columnKeys.value.map(key => ({ key, label: t(`groupView.fields.${key}`), sortable: sortableKeys.value.includes(key) })))
const columns = computed(() => allColumns.value.filter(column => !hiddenColumns.value.includes(column.key)))

// money 将对用户计费金额显示为美元。
function money(value: number) { return `$${Number(value || 0).toFixed(4)}` }
// dateTime 显示快照和日志时间，缺少快照时明确留空。
function dateTime(value?: string | number | null) {
  if (value === undefined || value === null || value === '') return '—'
  const parsed = new Date(typeof value === 'number' ? value * 1000 : value)
  return Number.isFinite(parsed.getTime()) ? parsed.toLocaleString() : '—'
}
// toggleColumn 控制当前页签的显示列，至少保留一列。
function toggleColumn(key: string) {
  if (hiddenColumns.value.includes(key)) hiddenColumns.value = hiddenColumns.value.filter(item => item !== key)
  else if (columns.value.length > 1) hiddenColumns.value.push(key)
}
// sort 使用服务端排序，保证跨分页结果一致。
function sort(key: string, order: 'asc' | 'desc') { sortBy.value = key; sortOrder.value = order; page.value = 1; scheduleLoad() }
// changePageSize 校验接口允许的分页大小并回到首页。
function changePageSize(value: number) {
  if (!Number.isInteger(value) || value < 1 || value > maxPageSize) return
  pageSize.value = value; page.value = 1; scheduleLoad()
}
// load 同时替换列表与汇总，任何失败都清空旧范围的数据。
async function load() {
  if (!groups.value.length) return
  controller?.abort()
  const current = new AbortController()
  controller = current
  loading.value = true
  rows.value = []
  total.value = 0
  summary.value = null
  error.value = ''
  const currentTab = tab.value
  const currentFilters = { ...filters.value }
  try {
    const [result, stats] = await Promise.all([
      listView(currentTab, { ...currentFilters, page: page.value, page_size: pageSize.value, sort_by: sortBy.value, sort_order: sortOrder.value }, current.signal),
      currentTab === 'accounts' ? Promise.resolve(null) : getSummary(currentFilters, current.signal)
    ])
    if (current.signal.aborted) return
    rows.value = result.items
    total.value = result.total
    summary.value = stats
  } catch (err) {
    if (!current.signal.aborted) error.value = err instanceof Error ? err.message : t('groupView.loadFailed')
  } finally {
    if (!current.signal.aborted) loading.value = false
  }
}
// scheduleLoad 合并筛选变化，立即清除旧数据并延迟发起读取。
function scheduleLoad() {
  controller?.abort()
  clearTimeout(timer)
  rows.value = []
  summary.value = null
  total.value = 0
  timer = setTimeout(load, 250)
}
// refresh 重新读取授权与页面数据，不调用账号探测或刷新凭据接口。
async function refresh() {
  controller?.abort()
  clearTimeout(timer)
  rows.value = []
  summary.value = null
  total.value = 0
  loading.value = true
  error.value = ''
  try {
    groups.value = await getAccess()
    const authorizedGroups = new Set(groups.value.map(group => group.id))
    const remainingGroups = selectedGroups.value.filter(id => authorizedGroups.has(id))
    if (remainingGroups.length !== selectedGroups.value.length) selectedGroups.value = remainingGroups
    page.value = 1
    await nextTick()
    clearTimeout(timer)
    if (!groups.value.length) { error.value = t('groupView.noAccess'); loading.value = false; return }
    await load()
  } catch (err) {
    error.value = err instanceof Error ? err.message : t('groupView.loadFailed')
    loading.value = false
  }
}
watch([selectedGroups, startDate, endDate], () => { userID.value = ''; apiKeyID.value = ''; accountID.value = ''; model.value = '' })
watch(tab, () => {
  hiddenColumns.value = []
  sortBy.value = tab.value === 'accounts' ? 'id' : tab.value === 'people' ? 'cost' : 'created_at'
  sortOrder.value = 'desc'
})
watch(filters, () => { page.value = 1; scheduleLoad() })
watch(page, scheduleLoad)
onMounted(refresh)
onBeforeUnmount(() => { controller?.abort(); clearTimeout(timer) })
</script>
