<template>
  <BaseDialog :show="show" :title="t('groupView.grantsTitle')" @close="emit('close')">
    <p class="mb-3 text-sm text-gray-500">{{ user?.email }}</p>
    <p class="mb-4 text-sm text-gray-500">{{ t('groupView.grantsHint') }}</p>
    <p v-if="error" class="mb-3 text-sm text-red-600" role="alert">{{ error }}</p>
    <LoadingSpinner v-if="loading" />
    <div v-else class="max-h-80 space-y-2 overflow-y-auto">
      <label v-for="group in groups" :key="group.id" class="flex items-center gap-3 rounded-lg border p-3 dark:border-dark-600">
        <input v-model="selected" type="checkbox" :value="group.id" :disabled="saving" />
        <span>{{ group.name }} <span class="text-gray-400">#{{ group.id }}</span></span>
      </label>
    </div>
    <template #footer>
      <button class="btn btn-secondary" :disabled="saving" @click="emit('close')">{{ t('common.cancel') }}</button>
      <button class="btn btn-primary" :disabled="loading || saving || !loaded" @click="save">{{ t('common.save') }}</button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import { getAllIncludingInactive } from '@/api/admin/groups'
import { getUserGrants, setUserGrants, type ViewGroup } from '@/api/groupView'
import type { AdminUser } from '@/types'

const props = defineProps<{ show: boolean; user: AdminUser | null }>()
const emit = defineEmits<{ close: []; success: [] }>()
const { t } = useI18n()
const groups = ref<ViewGroup[]>([])
const selected = ref<number[]>([])
const loading = ref(false)
const saving = ref(false)
const loaded = ref(false)
const error = ref('')
let generation = 0

watch(() => [props.show, props.user?.id] as const, async ([show, id]) => {
  const current = ++generation
  loaded.value = false
  error.value = ''
  selected.value = []
  if (!show || !id) return
  loading.value = true
  try {
    const [all, grants] = await Promise.all([getAllIncludingInactive(), getUserGrants(id)])
    if (current !== generation) return
    groups.value = all
    selected.value = grants.map(group => group.id)
    loaded.value = true
  } catch (err) {
    if (current === generation) error.value = err instanceof Error ? err.message : t('groupView.loadFailed')
  } finally {
    if (current === generation) loading.value = false
  }
}, { immediate: true })

// save 原子提交本次选择的完整分组集合。
async function save() {
  if (!props.user || !loaded.value) return
  saving.value = true
  error.value = ''
  try {
    await setUserGrants(props.user.id, selected.value)
    emit('success')
    emit('close')
  } catch (err) {
    error.value = err instanceof Error ? err.message : t('groupView.saveFailed')
  } finally {
    saving.value = false
  }
}
</script>
