<template>
  <div class="space-y-1">
    <label class="block text-xs text-gray-500" :for="`group-view-${kind}`">{{ label }}</label>
    <input v-model="search" class="input w-full text-sm" :aria-label="`${label} ${t('common.search')}`" :placeholder="t('common.search')" />
    <select :id="`group-view-${kind}`" class="input w-full text-sm" :value="modelValue" @change="emit('update:modelValue', ($event.target as HTMLSelectElement).value)">
      <option value="">{{ t('common.all') }}</option>
      <option v-if="modelValue && !options.some(item => item.id === modelValue)" :value="modelValue">{{ selectedLabel || modelValue }}</option>
      <option v-for="option in options" :key="option.id" :value="option.id">{{ option.label }}</option>
    </select>
    <div v-if="total > 20" class="flex items-center justify-between text-xs">
      <button :disabled="page === 1 || loading" @click="page--">{{ t('pagination.previous') }}</button>
      <span>{{ page }} / {{ Math.ceil(total / 20) }}</span>
      <button :disabled="page * 20 >= total || loading" @click="page++">{{ t('pagination.next') }}</button>
    </div>
    <p v-if="error" class="text-xs text-red-600" role="alert">{{ error }}</p>
  </div>
</template>

<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { getOptions, type OptionKind, type ViewOption, type ViewParams } from '@/api/groupView'

const props = defineProps<{ modelValue: string; kind: OptionKind; label: string; params: ViewParams }>()
const emit = defineEmits<{ 'update:modelValue': [value: string] }>()
const { t } = useI18n()
const search = ref('')
const page = ref(1)
const total = ref(0)
const options = ref<ViewOption[]>([])
const selectedLabel = ref('')
const loading = ref(false)
const error = ref('')
let controller: AbortController | undefined
let timer: ReturnType<typeof setTimeout> | undefined

// load 读取有界候选并丢弃过期请求，避免筛选切换后旧结果覆盖新范围。
async function load() {
  controller?.abort()
  const current = new AbortController()
  controller = current
  loading.value = true
  error.value = ''
  options.value = []
  try {
    const result = await getOptions({ ...props.params, kind: props.kind, option_search: search.value, page: page.value, page_size: 20 }, current.signal)
    if (current.signal.aborted) return
    options.value = result.items
    total.value = result.total
    selectedLabel.value = result.items.find(item => item.id === props.modelValue)?.label || selectedLabel.value
  } catch (err) {
    if (!current.signal.aborted) error.value = err instanceof Error ? err.message : t('groupView.loadFailed')
  } finally {
    if (!current.signal.aborted) loading.value = false
  }
}

watch(() => props.modelValue, value => {
  selectedLabel.value = options.value.find(item => item.id === value)?.label || ''
})
watch([() => props.params, search], () => {
  controller?.abort()
  clearTimeout(timer)
  options.value = []
  selectedLabel.value = ''
  total.value = 0
  page.value = 1
  timer = setTimeout(load, 250)
}, { immediate: true })
watch(page, () => { clearTimeout(timer); void load() })
onBeforeUnmount(() => { controller?.abort(); clearTimeout(timer) })
</script>
