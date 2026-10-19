<template>
  <BaseDialog :show="show" :title="t('admin.accounts.modelConsistency.title')" width="wide" @close="emit('close')">
    <div class="space-y-5">
      <p class="font-medium text-gray-900 dark:text-gray-100">{{ account?.name }}</p>
      <div class="space-y-2 rounded-xl bg-blue-50 p-3 text-sm text-blue-900 dark:bg-blue-900/20 dark:text-blue-200">
        <p>{{ t('admin.accounts.modelConsistency.description') }}</p>
        <p>{{ t('admin.accounts.modelConsistency.quotaNotice') }}</p>
      </div>

      <p v-if="requestError || modelsError" role="alert" class="text-sm text-red-600 dark:text-red-400">
        {{ requestError || modelsError }}
      </p>
      <p v-if="snapshot && !snapshot.supported" role="status" class="text-sm text-amber-700 dark:text-amber-300">
        {{ reasonLabel(snapshot.unsupported_reason || 'unsupported_account') }}
      </p>

      <fieldset v-if="snapshot?.supported" :disabled="busy || Boolean(activeRunId)" class="space-y-2">
        <legend class="text-sm font-medium text-gray-900 dark:text-gray-100">
          {{ t('admin.accounts.modelConsistency.selectModels', { count: selectedModels.length, max: MAX_MODELS }) }}
        </legend>
        <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.modelConsistency.referenceNotice') }}</p>
        <p v-if="snapshot.reference_models.length" class="break-words text-xs text-gray-600 dark:text-gray-300">
          {{ t('admin.accounts.modelConsistency.referenceModels') }}: {{ snapshot.reference_models.join(', ') }}
        </p>
        <div v-if="loadingModels" role="status" class="py-3 text-sm text-gray-500">{{ t('common.loading') }}</div>
        <div v-else-if="models.length" class="grid max-h-52 gap-2 overflow-y-auto rounded-lg border border-gray-200 p-3 sm:grid-cols-2 dark:border-dark-600">
          <label v-for="model in models" :key="model" class="flex min-w-0 items-start gap-2 rounded p-1 text-sm text-gray-800 dark:text-gray-200">
            <input
              v-model="selectedModels"
              type="checkbox"
              class="mt-0.5 h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500"
              :value="model"
              :disabled="selectedModels.length >= MAX_MODELS && !selectedModels.includes(model)"
            />
            <span class="break-all">{{ model }}</span>
          </label>
        </div>
        <p v-else class="text-sm text-gray-500">{{ t('admin.accounts.modelConsistency.noModels') }}</p>
      </fieldset>

      <div class="flex flex-wrap items-center gap-2">
        <button type="button" class="btn btn-primary" :disabled="!canStart" @click="startRun">
          <Icon v-if="starting" name="refresh" size="sm" class="mr-2 animate-spin" />
          {{ t('admin.accounts.modelConsistency.start') }}
        </button>
        <button v-if="activeRunId" type="button" class="btn btn-secondary" :disabled="busy || refreshing" @click="cancelRun">
          {{ t('admin.accounts.modelConsistency.cancel') }}
        </button>
        <button type="button" class="btn btn-secondary" :disabled="busy || refreshing || loadingModels" @click="refreshAll">
          <Icon name="refresh" size="sm" class="mr-2" :class="{ 'animate-spin': refreshing }" />
          {{ t('admin.accounts.modelConsistency.refresh') }}
        </button>
      </div>
      <p v-if="activeRunId" role="status" class="text-xs text-gray-500 dark:text-gray-400">
        {{ t('admin.accounts.modelConsistency.runningNotice') }}
      </p>

      <section class="space-y-3" :aria-label="t('admin.accounts.modelConsistency.latestResults')" :aria-busy="refreshing">
        <h4 class="text-sm font-semibold text-gray-900 dark:text-gray-100">{{ t('admin.accounts.modelConsistency.latestResults') }}</h4>
        <p v-if="!snapshot && refreshing" role="status" class="text-sm text-gray-500">{{ t('common.loading') }}</p>
        <p v-else-if="snapshot && !snapshot.results.length" class="text-sm text-gray-500">{{ t('admin.accounts.modelConsistency.noResults') }}</p>
        <article v-for="result in snapshot?.results || []" :key="result.requested_model" class="space-y-3 rounded-xl border border-gray-200 p-4 dark:border-dark-600">
          <div class="flex flex-wrap items-start justify-between gap-2">
            <dl class="min-w-0 space-y-1 text-sm">
              <div class="flex flex-wrap gap-x-2">
                <dt class="text-gray-500 dark:text-gray-400">{{ t('admin.accounts.modelConsistency.requestedModel') }}</dt>
                <dd class="break-all font-medium text-gray-900 dark:text-gray-100">{{ result.requested_model }}</dd>
              </div>
              <div class="flex flex-wrap gap-x-2">
                <dt class="text-gray-500 dark:text-gray-400">{{ t('admin.accounts.modelConsistency.upstreamModel') }}</dt>
                <dd class="break-all text-gray-900 dark:text-gray-100">{{ result.upstream_model || '—' }}</dd>
              </div>
            </dl>
            <span class="rounded-full px-2.5 py-1 text-xs font-medium" :class="statusClass(result)">
              {{ t(`admin.accounts.modelConsistency.status.${result.status}`) }}
            </span>
          </div>
          <p v-if="result.reason" class="text-sm text-gray-700 dark:text-gray-300">{{ reasonLabel(result.reason) }}</p>
          <p v-if="result.limitation" class="text-xs text-amber-700 dark:text-amber-300">{{ reasonLabel(result.limitation) }}</p>
          <p v-if="!result.applicable" class="rounded-lg bg-amber-50 p-2 text-sm text-amber-800 dark:bg-amber-900/20 dark:text-amber-300">
            {{ t('admin.accounts.modelConsistency.notApplicable') }}
          </p>
          <p v-else class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.modelConsistency.applicable') }}</p>
          <dl class="grid gap-x-4 gap-y-2 text-xs text-gray-600 sm:grid-cols-2 dark:text-gray-300">
            <div><dt class="inline">{{ t('admin.accounts.modelConsistency.startedAt') }}: </dt><dd class="inline">{{ formatTime(result.started_at) }}</dd></div>
            <div><dt class="inline">{{ t(result.completed_at ? 'admin.accounts.modelConsistency.completedAt' : 'admin.accounts.modelConsistency.deadlineAt') }}: </dt><dd class="inline">{{ formatTime(result.completed_at || result.deadline_at) }}</dd></div>
            <div><dt class="inline">{{ t('admin.accounts.modelConsistency.samples') }}: </dt><dd class="inline">{{ result.valid_sample_count }} / {{ result.sample_count }}</dd></div>
            <div><dt class="inline">{{ t('admin.accounts.modelConsistency.usage') }}: </dt><dd class="inline">{{ result.usage ? t('admin.accounts.modelConsistency.tokens', result.usage) : t('admin.accounts.modelConsistency.usageUnavailable') }}</dd></div>
          </dl>
          <details class="text-xs text-gray-600 dark:text-gray-300">
            <summary class="cursor-pointer rounded py-1 font-medium focus-visible:ring-2 focus-visible:ring-primary-500">{{ t('admin.accounts.modelConsistency.details') }}</summary>
            <p class="mt-2">{{ t('admin.accounts.modelConsistency.scoreNotice') }}</p>
            <dl class="mt-2 grid gap-2 break-words sm:grid-cols-2">
              <div><dt class="inline">{{ t('admin.accounts.modelConsistency.candidate') }}: </dt><dd class="inline">{{ result.candidate_model || '—' }}</dd></div>
              <div><dt class="inline">{{ t('admin.accounts.modelConsistency.score') }}: </dt><dd class="inline">{{ result.score == null ? '—' : result.score.toFixed(4) }}</dd></div>
              <div><dt class="inline">{{ t('admin.accounts.modelConsistency.transport') }}: </dt><dd class="inline">{{ result.transport || '—' }}</dd></div>
              <div v-if="result.transport_reason"><dt class="inline">{{ t('admin.accounts.modelConsistency.transportReason') }}: </dt><dd class="inline">{{ result.transport_reason }}</dd></div>
              <div><dt class="inline">{{ t('admin.accounts.modelConsistency.profile') }}: </dt><dd class="inline">{{ result.profile || '—' }}</dd></div>
              <div><dt class="inline">{{ t('admin.accounts.modelConsistency.bankVersion') }}: </dt><dd class="inline">{{ result.versions.bank }}</dd></div>
              <div><dt class="inline">{{ t('admin.accounts.modelConsistency.algorithmVersion') }}: </dt><dd class="inline">{{ result.versions.algorithm }}</dd></div>
              <div><dt class="inline">{{ t('admin.accounts.modelConsistency.challengeVersion') }}: </dt><dd class="inline">{{ result.versions.challenge }}</dd></div>
              <div><dt class="inline">{{ t('admin.accounts.modelConsistency.profileVersion') }}: </dt><dd class="inline">{{ result.versions.profile }}</dd></div>
            </dl>
          </details>
        </article>
      </section>
    </div>
    <template #footer>
      <button type="button" class="btn btn-secondary" @click="emit('close')">{{ t('common.close') }}</button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { Icon } from '@/components/icons'
import type { Account } from '@/types'
import type { ModelConsistencyResult, ModelConsistencySnapshot } from '@/types/modelConsistency'

const props = defineProps<{
  // 是否展示检测弹窗。
  show: boolean
  // 当前查看的完整账号配置。
  account: Account | null
}>()
const emit = defineEmits<{ close: [] }>()
const { t, te, locale } = useI18n()
const MAX_MODELS = 8
const POLL_INTERVAL_MS = 3000
const snapshot = ref<ModelConsistencySnapshot | null>(null)
const models = ref<string[]>([])
const selectedModels = ref<string[]>([])
const refreshing = ref(false)
const loadingModels = ref(false)
const starting = ref(false)
const cancelling = ref(false)
const requestError = ref('')
const modelsError = ref('')
const busy = computed(() => starting.value || cancelling.value)
const activeRunId = computed(() => snapshot.value?.results.find((result) => result.status === 'running')?.run_id)
const canStart = computed(() => snapshot.value?.supported && !busy.value && !refreshing.value && !loadingModels.value && !requestError.value && !modelsError.value && !activeRunId.value && selectedModels.value.length > 0 && selectedModels.value.length <= MAX_MODELS)
let generation = 0
let pollTimer: ReturnType<typeof setTimeout> | undefined
let resultController: AbortController | undefined

// 将服务端原因代码转换为可读说明，未知代码保留供排查。
function reasonLabel(reason: string): string {
  const key = `admin.accounts.modelConsistency.reasons.${reason}`
  return reason && te(key) ? t(key) : t('admin.accounts.modelConsistency.unknownReason', { reason: reason || '—' })
}

// 按当前语言展示服务端检测时间。
function formatTime(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '—' : date.toLocaleString(locale.value)
}

// 区分经验线索、失败和已不适用的历史结果。
function statusClass(result: ModelConsistencyResult): string {
  if (!result.applicable) return 'bg-gray-100 text-gray-700 dark:bg-dark-700 dark:text-gray-300'
  if (result.status === 'matched') return 'bg-green-100 text-green-800 dark:bg-green-900/30 dark:text-green-300'
  if (result.status === 'failed') return 'bg-red-100 text-red-800 dark:bg-red-900/30 dark:text-red-300'
  if (result.status === 'running') return 'bg-blue-100 text-blue-800 dark:bg-blue-900/30 dark:text-blue-300'
  return 'bg-amber-100 text-amber-800 dark:bg-amber-900/30 dark:text-amber-300'
}

// 停止尚未执行的自动刷新，防止关闭弹窗后继续轮询。
function stopPolling(): void {
  if (pollTimer) clearTimeout(pollTimer)
  pollTimer = undefined
}

// 读取服务端快照，并只在本弹窗仍有运行中批次时继续刷新。
async function refreshResults(): Promise<void> {
  if (!props.show || !props.account || refreshing.value) return
  stopPolling()
  const currentGeneration = generation
  const accountId = props.account.id
  const controller = new AbortController()
  resultController = controller
  refreshing.value = true
  requestError.value = ''
  try {
    const result = await adminAPI.accounts.getModelConsistency(accountId, controller.signal)
    if (currentGeneration !== generation) return
    snapshot.value = result
  } catch {
    if (currentGeneration === generation && !controller.signal.aborted) requestError.value = t('admin.accounts.modelConsistency.loadFailed')
  } finally {
    if (currentGeneration === generation) {
      refreshing.value = false
      if (activeRunId.value && !requestError.value) pollTimer = setTimeout(refreshResults, POLL_INTERVAL_MS)
    }
  }
}

// 读取目标账号已有模型列表，沿用账号映射和可用模型设置。
async function loadModels(): Promise<void> {
  if (!props.show || !props.account) return
  const currentGeneration = generation
  loadingModels.value = true
  modelsError.value = ''
  try {
    const available = await adminAPI.accounts.getAvailableModels(props.account.id)
    if (currentGeneration !== generation) return
    models.value = [...new Set(available.map((model) => model.id))]
    selectedModels.value = selectedModels.value.filter((model) => models.value.includes(model))
  } catch {
    if (currentGeneration === generation) modelsError.value = t('admin.accounts.modelConsistency.modelsFailed')
  } finally {
    if (currentGeneration === generation) loadingModels.value = false
  }
}

// 同时刷新模型列表和最近结果，不启动新的探测请求。
async function refreshAll(): Promise<void> {
  await Promise.all([refreshResults(), loadModels()])
}

// 启动管理员明确选择的模型批次，并读取后台任务的最新快照。
async function startRun(): Promise<void> {
  if (!canStart.value || !props.account) return
  const currentGeneration = generation
  starting.value = true
  requestError.value = ''
  stopPolling()
  try {
    const run = await adminAPI.accounts.startModelConsistency(props.account.id, [...selectedModels.value])
    if (currentGeneration !== generation || !snapshot.value) return
    const replaced = new Set(run.results.map((result) => result.requested_model))
    snapshot.value.results = [...run.results, ...snapshot.value.results.filter((result) => !replaced.has(result.requested_model))]
    await refreshResults()
  } catch {
    if (currentGeneration === generation) {
      await refreshResults()
      requestError.value = t('admin.accounts.modelConsistency.startFailed')
    }
  } finally {
    if (currentGeneration === generation) starting.value = false
  }
}

// 取消当前运行批次，重新读取结果以处理已完成或并发取消的情况。
async function cancelRun(): Promise<void> {
  if (!activeRunId.value || !props.account || busy.value) return
  const currentGeneration = generation
  cancelling.value = true
  requestError.value = ''
  stopPolling()
  try {
    await adminAPI.accounts.cancelModelConsistency(props.account.id, activeRunId.value)
    if (currentGeneration === generation) await refreshResults()
  } catch {
    if (currentGeneration === generation) requestError.value = t('admin.accounts.modelConsistency.cancelFailed')
  } finally {
    if (currentGeneration === generation) cancelling.value = false
  }
}

// 清理旧账号异步读取，避免弹窗切换后覆盖新账号的状态。
function resetRequests(): void {
  generation += 1
  stopPolling()
  resultController?.abort()
}

watch([() => props.show, () => props.account?.id], () => {
  resetRequests()
  snapshot.value = null
  models.value = []
  selectedModels.value = []
  refreshing.value = false
  loadingModels.value = false
  starting.value = false
  cancelling.value = false
  requestError.value = ''
  modelsError.value = ''
  if (props.show && props.account) void refreshAll()
}, { immediate: true })

onUnmounted(resetRequests)
</script>
