import type { InjectionKey } from 'vue'
import type { SettingsTab } from './components/ModelSettings.vue'

/** Open the app's settings at a tab, from anywhere on the page. */
export const openSettingsKey: InjectionKey<(tab?: SettingsTab) => void> = Symbol('openSettings')
