<template>
  <section class="card" aria-labelledby="gateway-timezone-title">
    <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
      <h2 id="gateway-timezone-title" class="text-lg font-semibold text-gray-900 dark:text-white">
        {{ t('admin.settings.gatewayTimezone.title') }}
      </h2>
      <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
        {{ t('admin.settings.gatewayTimezone.description') }}
      </p>
    </div>

    <div class="space-y-5 p-6" :aria-busy="busy">
      <p v-if="!settings && !loadError" class="text-sm text-gray-500" role="status">
        {{ t('common.loading') }}
      </p>
      <div v-if="loadError" class="flex flex-wrap items-center gap-3">
        <p class="text-sm text-red-600 dark:text-red-400" role="alert">{{ loadError }}</p>
        <button type="button" class="btn btn-secondary btn-sm" :disabled="busy" @click="loadSettings">
          {{ t('admin.settings.gatewayTimezone.retry') }}
        </button>
      </div>

      <template v-if="settings">
        <div class="grid gap-4 lg:grid-cols-2">
          <div class="rounded-xl border border-gray-200 bg-gray-50 p-4 dark:border-dark-600 dark:bg-dark-800">
            <div class="flex flex-wrap items-center gap-2">
              <span class="text-sm font-medium text-gray-600 dark:text-gray-300">
                {{ t('admin.settings.gatewayTimezone.currentTimezone') }}
              </span>
              <span v-if="!settings.is_set" class="rounded bg-gray-200 px-2 py-0.5 text-xs text-gray-600 dark:bg-dark-600 dark:text-gray-300">
                {{ t('admin.settings.gatewayTimezone.defaultValue') }}
              </span>
            </div>
            <p class="mt-3 break-all font-mono text-lg font-semibold text-gray-900 dark:text-white">
              {{ settings.timezone }}
            </p>
            <p class="mt-2 text-xs text-gray-500 dark:text-gray-400">
              {{ t('admin.settings.gatewayTimezone.currentHint') }}
            </p>
          </div>

          <div class="rounded-xl border border-gray-200 p-4 dark:border-dark-600">
            <div class="flex flex-wrap items-center justify-between gap-2">
              <span class="text-sm font-medium text-gray-600 dark:text-gray-300">
                {{ t('admin.settings.gatewayTimezone.detectedTimezone') }}
              </span>
              <button type="button" class="btn btn-secondary btn-sm" :disabled="busy" @click="refreshDetection(false)">
                {{ operation === 'detect' ? t('admin.settings.gatewayTimezone.detecting') : t('admin.settings.gatewayTimezone.refresh') }}
              </button>
            </div>
            <template v-if="settings.detection.ip">
              <p class="mt-3 break-all font-mono text-lg font-semibold text-gray-900 dark:text-white">
                {{ settings.detection.timezone }}
              </p>
              <p class="mt-2 break-all text-sm text-gray-600 dark:text-gray-300">
                <span class="font-mono">{{ settings.detection.ip }}</span>
                <span v-if="location"> · {{ location }}</span>
              </p>
              <p class="mt-2 text-xs text-gray-400 dark:text-gray-500">
                {{ t('admin.settings.gatewayTimezone.detectedAt', { time: detectedAt }) }}
              </p>
            </template>
            <p v-else class="mt-3 text-sm text-gray-500 dark:text-gray-400">
              {{ t('admin.settings.gatewayTimezone.noDetection') }}
            </p>
            <p v-if="detectionFailed" class="mt-3 text-sm text-red-600 dark:text-red-400" role="alert">
              {{ t(settings.detection.ip ? 'admin.settings.gatewayTimezone.detectionFailedWithPrevious' : 'admin.settings.gatewayTimezone.detectionFailed') }}
            </p>
          </div>
        </div>

        <div class="border-t border-gray-100 pt-5 dark:border-dark-700">
          <label for="gateway-timezone-select" class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">
            {{ t('admin.settings.gatewayTimezone.selectTimezone') }}
          </label>
          <div class="flex flex-col gap-3 sm:flex-row sm:items-center">
            <Select
              id="gateway-timezone-select"
              v-model="selectedTimezone"
              :options="timezoneOptions"
              :disabled="busy"
              :searchable="true"
              :aria-label="t('admin.settings.gatewayTimezone.selectTimezone')"
              aria-describedby="gateway-timezone-feedback"
              class="min-w-0 flex-1 sm:max-w-md"
            />
            <div class="flex flex-wrap gap-2">
              <button type="button" class="btn btn-primary" :disabled="busy || !selectedTimezone || (settings.is_set && !dirty)" @click="saveSelection">
                {{ operation === 'save' ? t('admin.settings.gatewayTimezone.saving') : t('admin.settings.gatewayTimezone.save') }}
              </button>
              <button type="button" class="btn btn-secondary" :disabled="busy" @click="refreshDetection(true)">
                {{ operation === 'auto' ? t('admin.settings.gatewayTimezone.detecting') : t('admin.settings.gatewayTimezone.autoSet') }}
              </button>
            </div>
          </div>
          <div id="gateway-timezone-feedback" class="mt-3 space-y-1.5 text-sm" aria-live="polite">
            <p v-if="dirty" class="text-amber-600 dark:text-amber-400">{{ t('admin.settings.gatewayTimezone.unsaved') }}</p>
            <p v-if="mismatch" class="text-red-600 dark:text-red-400">
              {{ t('admin.settings.gatewayTimezone.mismatch', { selected: selectedTimezone, detected: settings.detection.timezone }) }}
            </p>
            <p v-if="saveError" class="text-red-600 dark:text-red-400" role="alert">{{ saveError }}</p>
          </div>
        </div>
      </template>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Select from '@/components/common/Select.vue'
import {
  detectGatewayTimezone,
  getGatewayTimezoneSettings,
  saveGatewayTimezone,
  type GatewayTimezoneSettings,
} from '@/api/admin/gatewayTimezone'
import { useAppStore } from '@/stores'
import { extractApiErrorMessage } from '@/utils/apiError'
import { getTimezoneNames } from '@/utils/timezones'

const props = defineProps<{ active: boolean }>()
const { t, locale } = useI18n()
const appStore = useAppStore()
const settings = ref<GatewayTimezoneSettings | null>(null)
const selectedTimezone = ref('')
const operation = ref<'load' | 'detect' | 'save' | 'auto' | null>(null)
const loadError = ref('')
const saveError = ref('')
const detectionFailed = ref(false)
const busy = computed(() => operation.value !== null)
const dirty = computed(() => !!settings.value && selectedTimezone.value !== settings.value.timezone)
const mismatch = computed(() => !!settings.value?.detection.timezone && selectedTimezone.value !== settings.value.detection.timezone)
const location = computed(() => [settings.value?.detection.country, settings.value?.detection.region, settings.value?.detection.city].filter(Boolean).join(' / '))
const detectedAt = computed(() => settings.value?.detection.detected_at
  ? new Date(settings.value.detection.detected_at).toLocaleString(locale.value)
  : '')
const timezoneNames = getTimezoneNames()
const timezoneOptions = computed(() => [...new Set([
  ...timezoneNames, 'UTC', settings.value?.timezone, settings.value?.detection.timezone,
])].filter((value): value is string => !!value).sort().map(value => ({ value, label: value })))

// 首次加载已保存配置，再刷新服务器出口检测。
async function loadSettings(): Promise<void> {
  if (busy.value) return
  operation.value = 'load'
  loadError.value = ''
  try {
    settings.value = await getGatewayTimezoneSettings()
    selectedTimezone.value = settings.value.timezone
  } catch (error) {
    loadError.value = extractApiErrorMessage(error, t('admin.settings.gatewayTimezone.loadFailed'))
  } finally {
    operation.value = null
  }
  if (settings.value) await refreshDetection(false)
}

// 重新检测只更新展示，自动设置成功后才替换下拉值和已保存值。
async function refreshDetection(apply: boolean): Promise<void> {
  if (busy.value) return
  operation.value = apply ? 'auto' : 'detect'
  detectionFailed.value = false
  saveError.value = ''
  try {
    settings.value = await detectGatewayTimezone(apply)
    if (apply) {
      selectedTimezone.value = settings.value.timezone
      appStore.showSuccess(t('admin.settings.gatewayTimezone.saved'))
    }
  } catch {
    detectionFailed.value = true
    if (apply) appStore.showError(t('admin.settings.gatewayTimezone.autoFailed'))
  } finally {
    operation.value = null
  }
}

// 保存手动选择，保持最近检测结果独立。
async function saveSelection(): Promise<void> {
  if (busy.value) return
  operation.value = 'save'
  saveError.value = ''
  try {
    settings.value = await saveGatewayTimezone(selectedTimezone.value)
    selectedTimezone.value = settings.value.timezone
    appStore.showSuccess(t('admin.settings.gatewayTimezone.saved'))
  } catch (error) {
    saveError.value = extractApiErrorMessage(error, t('admin.settings.gatewayTimezone.saveFailed'))
  } finally {
    operation.value = null
  }
}

watch(() => props.active, (active) => {
  if (!active) return
  if (settings.value) void refreshDetection(false)
  else void loadSettings()
}, { immediate: true })
</script>
