import { computed, reactive } from 'vue'

function sidebar(key: string, width: number, minWidth: number) {
  const maxWidth = 480
  const constrain = (value: number) => Math.min(maxWidth, Math.max(minWidth, Math.round(value)))
  const state = reactive({ width, collapsed: false })
  try {
    const saved = JSON.parse(localStorage.getItem(key) ?? 'null') as {
      width?: unknown
      collapsed?: unknown
    } | null
    if (typeof saved?.width === 'number' && Number.isFinite(saved.width)) {
      state.width = constrain(saved.width)
    }
    state.collapsed = saved?.collapsed === true
  } catch {
    // The default layout also works when local storage is unavailable.
  }

  function save() {
    try {
      localStorage.setItem(key, JSON.stringify(state))
    } catch {
      // Keep the layout for this session.
    }
  }

  return { state, minWidth, maxWidth, constrain, save }
}

const sidebars = {
  projects: sidebar('ariadne.project-sidebar', 220, 160),
  files: sidebar('ariadne.diff-files-sidebar', 240, 144),
}

/** Remember a sidebar's pixel width and collapsed state for `DouSplitter` in `unit="px"` mode. */
export function useSidebarLayout(name: keyof typeof sidebars) {
  const { state, minWidth, maxWidth, constrain, save } = sidebars[name]
  const size = computed({
    get: () => state.width,
    set: (value) => {
      state.width = constrain(value)
    },
  })
  const collapsed = computed({
    get: () => state.collapsed,
    set: (value) => {
      state.collapsed = value
      save()
    },
  })

  return { size, min: minWidth, max: maxWidth, collapsed, save }
}
