<template>
  <AppLayout>
    <div class="space-y-6">
      <div>
        <h1 class="text-2xl font-bold text-gray-900 dark:text-white">{{ t('usageLeaderboard.title') }}</h1>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('usageLeaderboard.description') }}</p>
      </div>

      <div class="card overflow-hidden">
        <div class="flex flex-wrap items-end gap-4 border-b border-gray-100 px-4 py-4 dark:border-dark-700 sm:px-6">
          <div>
            <label for="leaderboard-period" class="mb-1 block text-sm text-gray-700 dark:text-gray-300">{{ t('usageLeaderboard.period') }}</label>
            <select id="leaderboard-period" v-model="period" class="input w-32">
              <option value="today">{{ t('usageLeaderboard.today') }}</option>
              <option value="yesterday">{{ t('usageLeaderboard.yesterday') }}</option>
              <option value="7d">{{ t('usageLeaderboard.last7Days') }}</option>
              <option value="30d">{{ t('usageLeaderboard.last30Days') }}</option>
            </select>
          </div>
          <div>
            <label for="leaderboard-limit" class="mb-1 block text-sm text-gray-700 dark:text-gray-300">{{ t('usageLeaderboard.limit') }}</label>
            <select id="leaderboard-limit" v-model.number="limit" class="input w-28">
              <option :value="50">Top 50</option>
              <option :value="100">Top 100</option>
            </select>
          </div>
          <div class="flex-1 text-xs text-gray-500 dark:text-gray-400">
            <p>{{ t('usageLeaderboard.timezone') }}</p>
            <p v-if="result" class="mt-1">{{ result.start_date }} — {{ result.end_date }}</p>
          </div>
          <button type="button" class="btn btn-secondary" :disabled="loading" @click="load">{{ t('common.refresh') }}</button>
        </div>

        <div v-if="error" role="alert" class="px-6 py-10 text-center text-sm text-red-600 dark:text-red-400">{{ error }}</div>
        <div v-else class="overflow-x-auto">
          <table class="w-full min-w-max divide-y divide-gray-200 dark:divide-dark-700" :aria-label="t('usageLeaderboard.title')">
            <thead class="bg-gray-50 dark:bg-dark-800">
              <tr>
                <th scope="col" class="w-16 px-4 py-3 text-left text-xs font-medium text-gray-500 sm:px-6">#</th>
                <th scope="col" class="px-4 py-3 text-left text-xs font-medium text-gray-500 dark:text-dark-400">{{ t('admin.usage.tokenRanking.columns.user') }}</th>
                <th
                  v-for="col in sortableColumns"
                  :key="col.key"
                  scope="col"
                  class="whitespace-nowrap px-4 py-3 text-right text-xs font-medium"
                  :class="sortBy === col.key ? 'text-primary-600 dark:text-primary-400' : 'text-gray-500 dark:text-dark-400'"
                  :aria-sort="sortBy === col.key ? 'descending' : 'none'"
                >
                  <button type="button" class="hover:text-primary-600" @click="sortBy = col.key">
                    {{ t(col.label) }} <span v-if="sortBy === col.key" aria-hidden="true">↓</span>
                  </button>
                </th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-200 bg-white dark:divide-dark-700 dark:bg-dark-900">
              <tr v-if="loading">
                <td :colspan="sortableColumns.length + 2" class="py-12 text-center"><LoadingSpinner /></td>
              </tr>
              <tr v-else-if="!result?.users.length">
                <td :colspan="sortableColumns.length + 2" class="py-12 text-center text-sm text-gray-400">{{ t('admin.dashboard.noDataAvailable') }}</td>
              </tr>
              <tr v-for="(item, index) in result?.users" v-else :key="index" class="hover:bg-gray-50 dark:hover:bg-dark-700/40">
                <td class="px-4 py-3 sm:px-6">
                  <span
                    class="inline-flex h-6 w-6 items-center justify-center rounded-full text-xs font-semibold"
                    :class="rankBadgeClasses[index] || 'text-gray-400'"
                  >{{ index + 1 }}</span>
                </td>
                <td class="max-w-[260px] truncate px-4 py-3 text-sm font-medium text-gray-700 dark:text-gray-200" :title="item.display_name">{{ item.display_name }}</td>
                <td
                  v-for="col in sortableColumns"
                  :key="col.key"
                  class="whitespace-nowrap px-4 py-3 text-right text-sm tabular-nums"
                  :class="col.key === 'actual_cost' ? 'font-medium text-green-600 dark:text-green-400' : 'text-gray-600 dark:text-gray-300'"
                  :title="item[col.key].toLocaleString()"
                >{{ formatMetric(item, col.key) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import {
  getUsageLeaderboard,
  type UsageLeaderboardItem,
  type UsageLeaderboardPeriod,
  type UsageLeaderboardResponse,
  type UsageLeaderboardSort
} from '@/api/usageLeaderboard'
import { formatCompactNumber, formatCostFixed } from '@/utils/format'

const { t } = useI18n()
const period = ref<UsageLeaderboardPeriod>('today')
const limit = ref<50 | 100>(50)
const sortBy = ref<UsageLeaderboardSort>('total_tokens')
const result = ref<UsageLeaderboardResponse | null>(null)
const loading = ref(false)
const error = ref('')
let requestSequence = 0
let controller: AbortController | undefined

const sortableColumns: { key: UsageLeaderboardSort; label: string }[] = [
  { key: 'requests', label: 'admin.usage.tokenRanking.columns.requests' },
  { key: 'input_tokens', label: 'admin.usage.tokenRanking.columns.inputTokens' },
  { key: 'output_tokens', label: 'admin.usage.tokenRanking.columns.outputTokens' },
  { key: 'cache_tokens', label: 'admin.usage.tokenRanking.columns.cacheTokens' },
  { key: 'total_tokens', label: 'admin.usage.tokenRanking.columns.totalTokens' },
  { key: 'actual_cost', label: 'admin.usage.tokenRanking.columns.cost' }
]
const rankBadgeClasses = [
  'bg-amber-100 text-amber-700 dark:bg-amber-500/20 dark:text-amber-400',
  'bg-gray-200 text-gray-600 dark:bg-gray-500/20 dark:text-gray-300',
  'bg-orange-100 text-orange-700 dark:bg-orange-500/20 dark:text-orange-400'
]

// 沿用管理端的数值格式，表格提示保留完整数值。
function formatMetric(item: UsageLeaderboardItem, key: UsageLeaderboardSort): string {
  if (key === 'actual_cost') return `$${formatCostFixed(item[key], 4)}`
  if (key === 'requests') return item[key].toLocaleString()
  return formatCompactNumber(item[key])
}

// 加载当前排行榜并取消旧请求，避免快速切换筛选时显示过期结果。
async function load() {
  const sequence = ++requestSequence
  controller?.abort()
  controller = new AbortController()
  loading.value = true
  result.value = null
  error.value = ''
  try {
    const data = await getUsageLeaderboard({ period: period.value, limit: limit.value, sort_by: sortBy.value }, controller.signal)
    if (sequence === requestSequence) result.value = data
  } catch {
    if (sequence === requestSequence) error.value = t('usageLeaderboard.loadError')
  } finally {
    if (sequence === requestSequence) loading.value = false
  }
}

watch([period, limit, sortBy], load, { immediate: true })
onUnmounted(() => {
  requestSequence++
  controller?.abort()
})
</script>
