import { useElementSize } from 'dou-ui'
import { computed, reactive, type Ref } from 'vue'

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

/** Keep the preferred width in pixels when the splitter moves between containers. */
export function useSidebarLayout(name: keyof typeof sidebars, container: Ref<HTMLElement | null>) {
  const { state, minWidth, maxWidth, constrain, save } = sidebars[name]
  const { width: containerWidth } = useElementSize(container)
  // DouSplitter divides the space on either side of its 1px divider.
  const available = computed(() => Math.max(1, containerWidth.value - 1))
  const percent = (pixels: number) => (pixels / available.value) * 100
  const max = computed(() => Math.min(45, percent(maxWidth)))
  const min = computed(() => Math.min(max.value, percent(minWidth)))
  const size = computed({
    get: () => Math.min(max.value, Math.max(min.value, percent(state.width))),
    set: (value) => {
      state.width = constrain((available.value * value) / 100)
    },
  })
  const collapsed = computed({
    get: () => state.collapsed,
    set: (value) => {
      state.collapsed = value
      save()
    },
  })

  return { containerWidth, size, min, max, collapsed, save }
}
