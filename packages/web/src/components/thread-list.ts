import { ref, watch } from 'vue'
import type { ThreadGroup, ThreadSort } from '../stage'

const SORT_KEY = 'ariadne.threadSort'
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

const sorts: ThreadSort[] = ['active', 'title', 'created']

/** How the thread list is sorted, remembered in this browser. */
export const threadSort = ref<ThreadSort>(
  load(SORT_KEY, 'active', (value): value is ThreadSort => sorts.includes(value as ThreadSort)),
)
watch(threadSort, (value) => keep(SORT_KEY, value))

/** The open groups of the thread list; finished threads start folded away. */
export const openGroups = ref<ThreadGroup[]>(
  load(OPEN_KEY, ['review', 'waiting', 'working', 'idle'], (value): value is ThreadGroup[] =>
    Array.isArray(value),
  ),
)
watch(openGroups, (value) => keep(OPEN_KEY, value))
