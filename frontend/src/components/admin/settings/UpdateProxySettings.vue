<template>
  <section class="card" aria-labelledby="update-proxy-title">
    <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
      <h2 id="update-proxy-title" class="text-lg font-semibold text-gray-900 dark:text-white">
        {{ t('admin.settings.updateProxy.title') }}
      </h2>
      <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
        {{ t('admin.settings.updateProxy.description') }}
      </p>
    </div>
    <div class="space-y-3 p-6">
      <label for="update-proxy-select" class="block text-sm font-medium text-gray-700 dark:text-gray-300">
        {{ t('admin.settings.updateProxy.title') }}
      </label>
      <Select
        id="update-proxy-select"
        v-model="model"
        :options="options"
        :disabled="!auth.can('settings.update')"
        :loading="loading"
        searchable
      />
      <p class="text-xs text-gray-500 dark:text-gray-400">
        {{ t('admin.settings.updateProxy.hint') }}
      </p>
      <p v-if="loadFailed" class="text-sm text-red-600 dark:text-red-400" role="alert">
        {{ t('admin.settings.updateProxy.loadFailed') }}
        <button type="button" class="ml-2 underline" :disabled="loading" @click="loadProxies">
          {{ t('admin.settings.gatewayTimezone.retry') }}
        </button>
      </p>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import Select from '@/components/common/Select.vue'
import { useAuthStore } from '@/stores/auth'
import type { Proxy } from '@/types'

const model = defineModel<number>({ default: 0 })
const { t } = useI18n()
const auth = useAuthStore()
const proxies = ref<Proxy[]>([])
const loading = ref(false)
const loadFailed = ref(false)
// 与后端约定：0 沿用启动配置，-1 本机直连，正数为代理 ID。
const options = computed(() => {
  const choices = [
    { value: -1, label: t('admin.settings.updateProxy.direct') },
    { value: 0, label: t('admin.settings.updateProxy.config') },
    ...proxies.value.map(proxy => ({ value: proxy.id, label: proxy.name }))
  ]
  if (model.value > 0 && !proxies.value.some(proxy => proxy.id === model.value)) {
    choices.push({
      value: model.value,
      label: t('admin.settings.updateProxy.unavailable', { id: model.value })
    })
  }
  return choices
})

// 加载可用代理，保留列表缺失时已保存的选择。
async function loadProxies() {
  if (!auth.can('proxies.read')) return
  loading.value = true
  loadFailed.value = false
  try {
    proxies.value = await adminAPI.proxies.getAll()
  } catch {
    loadFailed.value = true
  } finally {
    loading.value = false
  }
}

onMounted(loadProxies)
</script>
