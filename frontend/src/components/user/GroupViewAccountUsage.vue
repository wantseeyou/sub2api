<template>
  <div class="min-w-48 space-y-1.5">
    <p class="text-[10px] text-gray-400 dark:text-gray-500">{{ t('groupView.sharedQuota') }}</p>
    <p v-if="!account.quota?.length" class="text-xs text-gray-400">{{ t('groupView.noSnapshot') }}</p>
    <div v-for="window in account.quota" :key="window.name" :title="snapshotTitle(window)">
      <UsageProgressBar
        :label="windowLabel(window.name)"
        :utilization="window.used_percent"
        :resets-at="resetTime(window.resets_at)"
        :color="window.name.includes('7d') || window.name.includes('weekly') || window.name.includes('seven_day') ? 'emerald' : window.name.includes('monthly') ? 'purple' : 'indigo'"
        :show-now-when-idle="true"
        label-width="auto"
      />
    </div>
    <div v-if="account.platform === 'openai' && account.type === 'oauth'" class="flex flex-wrap items-center gap-1.5 text-[10px]">
      <span class="rounded bg-blue-50 px-1.5 py-0.5 font-medium text-blue-600 dark:bg-blue-900/30 dark:text-blue-400" :title="creditExpiries.map(value => formatDateTime(value)).join('\n')">
        {{ t('admin.accounts.openaiQuotaReset.count') }} {{ resetCount ?? t('common.unknown') }}
      </span>
      <span v-if="resetCount && creditExpiries[0]" class="text-gray-400" :title="formatDateTime(creditExpiries[0])">
        {{ t('groupView.creditExpires', { time: formatCountdown(creditExpiries[0]) }) }}
      </span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useIntervalFn } from '@vueuse/core'
import { useI18n } from 'vue-i18n'
import UsageProgressBar from '@/components/account/UsageProgressBar.vue'
import type { QuotaWindow, ViewRow } from '@/api/groupView'
import { formatCountdown, formatDateTime } from '@/utils/format'

const props = defineProps<{ account: ViewRow }>()
const { t } = useI18n()
const now = ref(Date.now())
// 与窗口倒计时同步推进重置卡到期判断，避免页面停留后仍显示已过期次数。
useIntervalFn(() => { now.value = Date.now() }, 60_000)
const creditExpiries = computed(() => (props.account.reset_credits?.credits ?? [])
  .map(credit => credit.expires_at)
  .filter(value => Number.isFinite(Date.parse(value)) && Date.parse(value) > now.value)
  .sort((a, b) => Date.parse(a) - Date.parse(b)))
const resetCount = computed(() => {
  const count = props.account.reset_credits?.available_count
  if (typeof count !== 'number' || !Number.isFinite(count) || count < 0) return null
  if (count === 0) return 0
  return creditExpiries.value.length ? Math.min(Math.floor(count), creditExpiries.value.length) : null
})

// windowLabel 使用账号管理中的窗口名称，隐藏内部快照前缀。
function windowLabel(name: string): string {
  if (name.endsWith('_7d_oi')) return '7d F'
  if (name.endsWith('_five_hour')) return '5h'
  if (name.endsWith('_seven_day') || name.endsWith('_weekly')) return '7d'
  if (name.endsWith('_monthly')) return '30d'
  if (name === 'grok_requests') return t('groupView.windowRequests')
  if (name === 'grok_tokens') return 'Token'
  return name.split('_').at(-1) || name
}

// resetTime 仅向共享进度条传递有效时间，避免异常快照显示 NaN。
function resetTime(value: QuotaWindow['resets_at']): string | null {
  if (value === undefined || value === null || value === '') return null
  const date = new Date(value)
  return Number.isFinite(date.getTime()) ? date.toISOString() : null
}

// snapshotTitle 将完整重置时间及采样时间放入提示，保持用量单元格紧凑。
function snapshotTitle(window: QuotaWindow): string {
  const reset = resetTime(window.resets_at)
  const updated = resetTime(window.updated_at)
  return `${t('groupView.resetsAt')}: ${reset ? formatDateTime(reset) : '-'}\n${t('groupView.updatedAt')}: ${updated ? formatDateTime(updated) : '-'}`
}
</script>
