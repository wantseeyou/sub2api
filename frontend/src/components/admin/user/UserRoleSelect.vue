<template>
  <div>
    <select
      :value="modelValue"
      :disabled="loading || !auth.isAdmin"
      class="input"
      :aria-label="t('admin.users.form.roleLabel')"
      @change="emit('update:modelValue', ($event.target as HTMLSelectElement).value)"
    >
      <option value="user">{{ t('admin.users.roles.user') }}</option>
      <option value="admin">{{ t('admin.users.roles.admin') }}</option>
      <option v-for="role in options" :key="role.id" :value="`role:${role.id}`" :disabled="!role.enabled">
        {{ role.name }}{{ !role.enabled ? ` (${t('roles.disabled')})` : '' }}
      </option>
    </select>
    <p v-if="error" role="alert" class="mt-1 text-sm text-red-600">{{ error }}</p>
    <button v-if="error" type="button" class="mt-1 text-sm text-primary-600" @click="load">{{ t('common.tryAgain') }}</button>
    <p v-if="!auth.isAdmin" class="input-hint">{{ t('roles.onlyAdminAssign') }}</p>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import rolesAPI from '@/api/admin/roles'
import type { AdminRole } from '@/types'

const props = defineProps<{ modelValue: string; currentRole?: AdminRole | null }>()
const emit = defineEmits<{ 'update:modelValue': [value: string] }>()
const { t } = useI18n()
const auth = useAuthStore()
const roles = ref<AdminRole[]>([])
const loading = ref(false)
const error = ref('')
const options = computed(() => props.currentRole && !roles.value.some(role => role.id === props.currentRole?.id)
  ? [...roles.value, props.currentRole] : roles.value)

// load 获取可选角色并保留当前已分配的停用角色。
async function load() {
  if (!auth.isAdmin) return
  loading.value = true
  error.value = ''
  try {
    roles.value = (await rolesAPI.list(1, 1000)).items
  } catch (err) {
    error.value = err instanceof Error ? err.message : t('roles.loadFailed')
  } finally {
    loading.value = false
  }
}
onMounted(load)
</script>
