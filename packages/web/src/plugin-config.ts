import { shallowRef } from 'vue'

export const activePlugin = shallowRef<string | null>(null)
export const pluginRevision = shallowRef(0)

export function openPluginSettings(id: string) {
  activePlugin.value = id
}

export function pluginsChanged() {
  pluginRevision.value++
}
