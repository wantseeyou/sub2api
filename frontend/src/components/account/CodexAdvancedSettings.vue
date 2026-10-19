<template>
  <section class="space-y-4 border-t border-gray-200 pt-4 dark:border-dark-600">
    <div class="flex items-start justify-between gap-4">
      <div>
        <p class="input-label mb-0">{{ t('admin.accounts.openai.httpToWs') }}</p>
        <p class="input-hint">{{ t('admin.accounts.openai.httpToWsDesc') }}</p>
      </div>
      <Toggle
        v-model="httpToWs"
        :aria-label="t('admin.accounts.openai.httpToWs')"
        data-testid="codex-http-to-ws-toggle"
      />
    </div>
    <div class="flex items-start justify-between gap-4">
      <div>
        <p class="input-label mb-0">{{ t('admin.accounts.openai.threadIsolation') }}</p>
        <p class="input-hint">{{ t('admin.accounts.openai.threadIsolationDesc') }}</p>
        <p v-if="threadIsolation" class="mt-2 text-xs text-amber-700 dark:text-amber-300" role="status">
          {{ t('admin.accounts.openai.threadIsolationNewSession') }}
        </p>
      </div>
      <Toggle
        v-model="threadIsolation"
        :aria-label="t('admin.accounts.openai.threadIsolation')"
        data-testid="codex-thread-isolation-toggle"
      />
    </div>
  </section>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import Toggle from '@/components/common/Toggle.vue'

const httpToWs = defineModel<boolean>('httpToWs', { required: true })
const threadIsolation = defineModel<boolean>('threadIsolation', { required: true })
const { t } = useI18n()
</script>
