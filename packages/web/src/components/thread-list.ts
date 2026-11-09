import { ref, watch } from 'vue'
import type { ThreadGroup } from '../stage'

const OPEN_KEY = 'ariadne.threadGroups'

function load<T>(key: string, fallback: T, valid: (value: unknown) => value is T): T {
  try {
    const raw = localStorage.getItem(key)
    if (raw === null) return fallback
    const value: unknown = JSON.parse(raw)
    return valid(value) ? value : fallback
  } catch {
    return fallback
  }
}

function keep(key: string, value: unknown) {
  try {
    localStorage.setItem(key, JSON.stringify(value))
  } catch {
    // Not remembered in this browser; that is fine.
  }
}

/** The open groups of the thread list; finished threads start folded away. */
export const openGroups = ref<ThreadGroup[]>(
  load(
    OPEN_KEY,
    ['review', 'waiting', 'working', 'paused', 'idle'],
    (value): value is ThreadGroup[] => Array.isArray(value),
  ),
)
watch(openGroups, (value) => keep(OPEN_KEY, value))
