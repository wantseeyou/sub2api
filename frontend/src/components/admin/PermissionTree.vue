<template>
  <div class="space-y-3">
    <input v-model="search" type="search" class="input" :placeholder="t('roles.searchPermissions')" :aria-label="t('roles.searchPermissions')" />
    <div class="flex flex-wrap items-center justify-between gap-2 text-sm">
      <span class="text-gray-500">{{ t('roles.selectedCount', { count: modelValue.length }) }}</span>
      <div class="flex gap-3">
        <button type="button" class="text-primary-600" @click="setVisible(true)">{{ t('roles.selectVisible') }}</button>
        <button type="button" class="text-gray-500" @click="setVisible(false)">{{ t('roles.clearVisible') }}</button>
      </div>
    </div>
    <p class="text-xs text-gray-500">{{ t('roles.sensitiveHint') }}</p>
    <div class="max-h-[45vh] space-y-3 overflow-y-auto rounded-xl border border-gray-200 p-3 dark:border-dark-600">
      <fieldset v-for="group in groups" :key="group.category" class="rounded-lg border border-gray-200 p-3 dark:border-dark-600">
        <legend class="px-1">
          <label class="flex cursor-pointer items-center gap-2 text-sm font-medium">
            <input type="checkbox" :checked="group.items.every(item => selected.has(item.id))" :indeterminate.prop="group.items.some(item => selected.has(item.id)) && !group.items.every(item => selected.has(item.id))" @change="toggle(group.items, ($event.target as HTMLInputElement).checked)" />
            {{ group.category }}
          </label>
        </legend>
        <div class="grid gap-2 sm:grid-cols-2">
          <label v-for="permission in group.items" :key="permission.id" class="flex cursor-pointer items-start gap-2 rounded-md p-1 text-sm hover:bg-gray-50 dark:hover:bg-dark-700">
            <input type="checkbox" class="mt-0.5" :checked="selected.has(permission.id)" @change="toggle([permission], ($event.target as HTMLInputElement).checked)" />
            <span>{{ permission.label }}<span v-if="permission.sensitive" class="ml-1 text-xs text-amber-600">{{ t('roles.sensitive') }}</span><span class="block break-all text-xs text-gray-400">{{ permission.id }}</span></span>
          </label>
        </div>
      </fieldset>
      <p v-if="!groups.length" class="py-4 text-center text-sm text-gray-500">{{ t('empty.noData') }}</p>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { AdminPermission } from '@/types'

const props = defineProps<{ modelValue: string[]; permissions: AdminPermission[] }>()
const emit = defineEmits<{ 'update:modelValue': [value: string[]] }>()
const { t } = useI18n()
const search = ref('')
const selected = computed(() => new Set(props.modelValue))
const groups = computed(() => {
  const query = search.value.trim().toLowerCase()
  const categories = new Map<string, AdminPermission[]>()
  for (const permission of props.permissions) {
    if (query && !`${permission.category} ${permission.label} ${permission.id}`.toLowerCase().includes(query)) continue
    const items = categories.get(permission.category) || []
    items.push(permission)
    categories.set(permission.category, items)
  }
  return [...categories].map(([category, items]) => ({ category, items }))
})

// toggle 仅修改指定权限，保留搜索范围外的选择。
function toggle(items: AdminPermission[], checked: boolean) {
  const next = new Set(props.modelValue)
  items.forEach(item => checked ? next.add(item.id) : next.delete(item.id))
  emit('update:modelValue', [...next])
}

// setVisible 批量选择或清除当前搜索结果。
function setVisible(checked: boolean) {
  toggle(groups.value.flatMap(group => group.items), checked)
}
</script>
