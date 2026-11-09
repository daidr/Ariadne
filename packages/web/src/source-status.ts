/**
 * Where the open project's files are, in one line, and whether its thread commands run on
 * another machine. Shared by the header and the project settings.
 */
import { computed, ref, watch } from 'vue'
import { api, type SourceKind } from './api'
import { state } from './store'

const kinds = ref<SourceKind[]>([])

const source = computed(() => state.detail?.project.source ?? null)

let asked = false
/**
 * Ask which kinds of source there are. Asked again with
 * each project opened and each time its settings open: plugins come and go while the page
 * stays, and a request that failed is tried anew.
 */
function load() {
  asked = true
  api.sourceKinds().then(
    (result) => {
      kinds.value = result.kinds
    },
    () => {},
  )
}
watch(
  () => [state.detail?.project.id, state.projectSettingsOpen],
  () => {
    if (asked) load()
  },
)
/** The provider of the stored source, once the kinds are known. */
const storedKind = computed(() => kinds.value.find((k) => k.kind === source.value?.kind) ?? null)
// A remote source runs commands on another machine.
const remote = computed(() => !!storedKind.value?.remote)
/**
 * One line for a source: the provider's summary filled in (e.g. `host:path`), or else its
 * first plain setting, e.g. the path or the repository.
 */
function summaryOf(of: SourceKind | null, settings: Record<string, unknown>): string | null {
  const template = of?.summary
  if (template) {
    return template.replace(/\{(\w+)\}/g, (_, key: string) => String(settings[key] ?? ''))
  }
  const value = Object.values(settings).find((v) => typeof v === 'string' && v)
  return typeof value === 'string' ? value : null
}
const summary = computed(() => {
  const own = summaryOf(storedKind.value, source.value?.settings ?? {})
  const hosting = source.value?.hosting
  if (!hosting) return own
  const hostingOf = kinds.value.find((k) => k.kind === hosting.kind) ?? null
  return [summaryOf(hostingOf, hosting.settings), own].filter(Boolean).join(' · ')
})
const remoteHost = computed(() => {
  const settings = source.value?.settings ?? {}
  const host = settings.host ?? settings.address
  return typeof host === 'string' ? host : (summary.value ?? '')
})

export function useSourceStatus() {
  if (!asked) load()
  return { source, summary, remote, remoteHost }
}
