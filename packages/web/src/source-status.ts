/**
 * Where the open project's files are, in one line, and what the user should know about
 * how its thread commands are isolated. Shared by the header and the project settings.
 */
import { computed, ref } from 'vue'
import { api, type SandboxStatus, type SourceKind } from './api'
import { state } from './store'

const kinds = ref<SourceKind[]>([])
/** Whether `kinds` came back: until then a remote source cannot be told from a local one. */
const kindsKnown = ref(false)

const source = computed(() => state.detail?.project.source ?? null)

// Without a sandbox, thread commands can read Ariadne's token; the user should know.
const sandbox = ref<SandboxStatus | null>(null)
let asked = false
/** Ask once what isolates commands here and which kinds of source there are. */
function load() {
  if (asked) return
  asked = true
  api.getSandbox().then(
    (result) => (sandbox.value = result.sandbox),
    () => {},
  )
  api.sourceKinds().then(
    (result) => {
      kinds.value = result.kinds
      kindsKnown.value = true
    },
    () => {},
  )
}
/** The provider of the stored source, once the kinds are known. */
const storedKind = computed(() => kinds.value.find((k) => k.kind === source.value?.kind) ?? null)
// A remote source runs commands on another machine; this one's sandbox is beside the point.
const remote = computed(() => !!storedKind.value?.remote)
// Notes about this machine's sandbox wait for the kinds, so a remote source never shows one.
const local = computed(() => !!source.value && kindsKnown.value && !remote.value)
const unsandboxed = computed(() => local.value && sandbox.value?.kind === null)
const sandboxNote = computed(() =>
  sandbox.value?.mode === 'off'
    ? 'sandbox.off.body'
    : sandbox.value?.platform === 'win32'
      ? 'sandbox.windows.body'
      : 'sandbox.missing.body',
)
// macOS's sandbox keeps files out of reach but not the server's processes.
const weaker = computed(() => local.value && sandbox.value?.kind === 'sandbox-exec')
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
  load()
  return { source, sandbox, summary, remote, remoteHost, unsandboxed, weaker, sandboxNote }
}
