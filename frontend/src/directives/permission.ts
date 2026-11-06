import { shallowRef, watchEffect, type ObjectDirective, type ShallowRef } from 'vue'
import { useAuthStore } from '@/stores/auth'

const states = new WeakMap<HTMLElement, { permissions: ShallowRef<string[]>; stop: () => void }>()

// permissionDirective 根据显式权限隐藏操作入口，并阻止无权限点击。
export const permissionDirective: ObjectDirective<HTMLElement, string | string[]> = {
  beforeMount(element, binding) {
    const auth = useAuthStore()
    const originalDisplay = element.style.display
    const originalTabIndex = element.getAttribute('tabindex')
    const permissions = shallowRef(Array.isArray(binding.value) ? binding.value : [binding.value])
    const check = () => permissions.value.some(permission => auth.can(permission))
    const stop = watchEffect(() => {
      const allowed = check()
      if (binding.modifiers.disable) {
        element.inert = !allowed
        element.setAttribute('aria-disabled', String(!allowed))
        if (!allowed) element.setAttribute('tabindex', '-1')
        else if (originalTabIndex === null) element.removeAttribute('tabindex')
        else element.setAttribute('tabindex', originalTabIndex)
      } else {
        element.style.display = allowed ? originalDisplay : 'none'
        element.hidden = !allowed
      }
    }, { flush: 'sync' })
    const block = (event: Event) => {
      if (!check()) { event.preventDefault(); event.stopImmediatePropagation() }
    }
    element.addEventListener('click', block, true)
    element.addEventListener('submit', block, true)
    states.set(element, { permissions, stop: () => { stop(); element.removeEventListener('click', block, true); element.removeEventListener('submit', block, true) } })
  },
  updated(element, binding) {
    const state = states.get(element)
    if (state) state.permissions.value = Array.isArray(binding.value) ? binding.value : [binding.value]
  },
  unmounted(element) { states.get(element)?.stop(); states.delete(element) }
}
