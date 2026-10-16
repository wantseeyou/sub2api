<template>
  <BaseDialog :show="true" :title="t('admin.proxies.timezone.title')" :show-close-button="!busy" :close-on-escape="!busy" @close="emit('close')">
    <div class="space-y-5">
      <p class="text-sm text-gray-500 dark:text-gray-400">{{ proxy.name }} · {{ t('admin.proxies.timezone.description') }}</p>
      <div class="rounded-xl bg-gray-50 p-4 dark:bg-dark-800">
        <p class="text-sm text-gray-500">{{ t('admin.proxies.timezone.current') }}</p>
        <p class="mt-1 break-all font-mono font-medium">{{ settings.timezone || t('admin.proxies.timezone.unset') }}</p>
      </div>
      <div class="rounded-xl border border-gray-200 p-4 dark:border-dark-600">
        <div class="flex items-center justify-between gap-2">
          <span class="text-sm font-medium">{{ t('admin.proxies.timezone.detected') }}</span>
          <button type="button" class="btn btn-secondary btn-sm" :disabled="busy" @click="detect(false)">
            {{ operation === 'detect' ? t('admin.proxies.timezone.detecting') : t('admin.proxies.timezone.refresh') }}
          </button>
        </div>
        <template v-if="settings.timezone_detection?.ip">
          <p class="mt-3 break-all font-mono font-medium">{{ settings.timezone_detection.timezone }}</p>
          <p class="mt-2 break-all text-sm text-gray-600 dark:text-gray-300">{{ settings.timezone_detection.ip }} · {{ location }}</p>
          <p class="mt-2 text-xs text-gray-500">{{ t('admin.proxies.timezone.detectedAt', { time: detectedAt }) }}</p>
        </template>
        <p v-else class="mt-3 text-sm text-gray-500">{{ t('admin.proxies.timezone.noDetection') }}</p>
      </div>
      <div>
        <label for="proxy-timezone-select" class="input-label">{{ t('admin.proxies.timezone.select') }}</label>
        <Select id="proxy-timezone-select" v-model="selectedTimezone" :options="options" :searchable="true" :disabled="busy" :placeholder="t('admin.proxies.timezone.select')" :aria-label="t('admin.proxies.timezone.select')" />
        <p v-if="selectedTimezone && selectedTimezone !== settings.timezone" class="mt-2 text-sm text-amber-600">{{ t('admin.proxies.timezone.unsaved') }}</p>
        <p v-if="mismatch" class="mt-2 text-sm text-red-600 dark:text-red-400" role="status">{{ t('admin.proxies.timezone.mismatch', { detected: settings.timezone_detection?.timezone }) }}</p>
      </div>
      <p v-if="error" class="text-sm text-red-600 dark:text-red-400" role="alert">{{ error }}</p>
    </div>
    <template #footer>
      <div class="flex flex-wrap justify-end gap-2">
        <button type="button" class="btn btn-secondary" :disabled="busy" @click="detect(true)">{{ operation === 'auto' ? t('admin.proxies.timezone.detecting') : t('admin.proxies.timezone.auto') }}</button>
        <button type="button" class="btn btn-primary" :disabled="busy || !selectedTimezone || selectedTimezone === settings.timezone" @click="save">{{ operation === 'save' ? t('common.saving') : t('common.save') }}</button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { Proxy } from '@/types'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Select from '@/components/common/Select.vue'
import { detectProxyTimezone, saveProxyTimezone } from '@/api/admin/proxies'
import { getTimezoneNames } from '@/utils/timezones'
import { useAppStore } from '@/stores/app'

const props = defineProps<{ proxy: Proxy }>()
const emit = defineEmits<{ close: []; updated: [proxy: Proxy] }>()
const { t, locale } = useI18n()
const appStore = useAppStore()
const settings = ref(props.proxy)
const selectedTimezone = ref(props.proxy.timezone || '')
const operation = ref<'save' | 'detect' | 'auto' | null>(null)
const busy = computed(() => operation.value !== null)
const error = ref('')
const timezoneNames = getTimezoneNames()
const options = computed(() => [...new Set([...timezoneNames, 'UTC', settings.value.timezone, settings.value.timezone_detection?.timezone])]
  .filter((value): value is string => !!value).sort().map(value => ({ value, label: value })))
const location = computed(() => [settings.value.timezone_detection?.country, settings.value.timezone_detection?.region, settings.value.timezone_detection?.city].filter(Boolean).join(' / '))
const detectedAt = computed(() => settings.value.timezone_detection?.detected_at
  ? new Date(settings.value.timezone_detection.detected_at).toLocaleString(locale.value) : '')
const mismatch = computed(() => !!selectedTimezone.value && !!settings.value.timezone_detection?.timezone && selectedTimezone.value !== settings.value.timezone_detection.timezone)

// 保存手动选择，保持检测快照不变。
async function save(): Promise<void> {
  if (busy.value) return
  operation.value = 'save'
  error.value = ''
  try {
    settings.value = await saveProxyTimezone(props.proxy.id, selectedTimezone.value)
    selectedTimezone.value = settings.value.timezone || ''
    emit('updated', settings.value)
    appStore.showSuccess(t('admin.proxies.timezone.saved'))
  } catch {
    error.value = t('admin.proxies.timezone.saveFailed')
  } finally {
    operation.value = null
  }
}

// 检测仅刷新快照，自动设置成功后再同步选择框。
async function detect(apply: boolean): Promise<void> {
  if (busy.value) return
  operation.value = apply ? 'auto' : 'detect'
  error.value = ''
  try {
    settings.value = await detectProxyTimezone(props.proxy.id, apply)
    if (apply) selectedTimezone.value = settings.value.timezone || ''
    emit('updated', settings.value)
    if (apply) appStore.showSuccess(t('admin.proxies.timezone.saved'))
  } catch {
    error.value = t('admin.proxies.timezone.detectFailed')
  } finally {
    operation.value = null
  }
}
</script>
