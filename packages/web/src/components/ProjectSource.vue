<script setup lang="ts">
import { DouAlert, DouButton, DouPopover, DouText } from 'dou-ui'
import { FolderGitIcon, FolderOpenIcon, PencilIcon, ServerIcon } from '@dou-ui/icons'
import { computed, onMounted, ref, watch } from 'vue'
import { api, type SandboxStatus, type SourceKind } from '../api'
import { t } from '../i18n'
import { openProjectSettings, state } from '../store'

const open = ref(false)
const kinds = ref<SourceKind[]>([])
/** Whether `kinds` came back: until then a remote source cannot be told from a local one. */
const kindsKnown = ref(false)

// With no source there is nothing to look at, so opening goes straight to the form.
watch(open, (isOpen) => {
  if (isOpen && !state.detail?.project.source) edit()
})

const source = computed(() => state.detail?.project.source ?? null)

// Without a sandbox, thread commands can read Ariadne's token; the user should know.
const sandbox = ref<SandboxStatus | null>(null)
onMounted(() => {
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
})
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
const isRepo = computed(
  () => !!storedKind.value?.provides.includes('repo') || !!source.value?.hosting,
)
const remoteHost = computed(() => {
  const settings = source.value?.settings ?? {}
  const host = settings.host ?? settings.address
  return typeof host === 'string' ? host : (summary.value ?? '')
})

/** The form is in the project settings; the header only shows where the files are. */
function edit() {
  open.value = false
  openProjectSettings('source')
}

/** A long path cut in the middle: its start and the folder it ends in both stay readable. */
function middle(text: string, max = 44): string {
  if (text.length <= max) return text
  const tail = Math.ceil(max * 0.6)
  return `${text.slice(0, max - tail - 1)}…${text.slice(-tail)}`
}
</script>

<template>
  <DouPopover
    v-if="state.detail"
    v-model:open="open"
    :title="t('project.source.title')"
    :width="360"
    placement="bottom-end"
  >
    <!-- Without a sandbox the button is the only sign left in view, so it turns to a warning. -->
    <DouButton
      variant="ghost"
      size="sm"
      :tone="unsandboxed ? 'warning' : undefined"
      :aria-label="t('project.source.title')"
      :title="summary ?? t('project.source.title')"
    >
      <ServerIcon v-if="source && remote" />
      <FolderGitIcon v-else-if="source && isRepo" />
      <FolderOpenIcon v-else />
    </DouButton>
    <template #content>
      <div class="source">
        <div class="source-row">
          <DouText v-if="summary" size="sm" :title="summary" class="source-path">
            {{ middle(summary) }}
          </DouText>
          <DouText v-else variant="tertiary" size="sm" class="source-path">
            {{ t('project.source.none') }}
          </DouText>
          <DouButton
            size="sm"
            variant="ghost"
            :aria-label="t('project.source.edit')"
            :title="t('project.source.edit')"
            @click="edit"
          >
            <PencilIcon />
          </DouButton>
        </div>
        <DouAlert
          v-if="unsandboxed && sandbox"
          tone="warning"
          role="alert"
          :title="t(sandbox.mode === 'off' ? 'sandbox.off.title' : 'sandbox.missing.title')"
        >
          {{ t(sandboxNote) }}
        </DouAlert>
        <DouText v-else-if="weaker" variant="tertiary" size="sm">{{ t('sandbox.weaker') }}</DouText>
        <DouText v-else-if="source && remote" variant="tertiary" size="sm">
          {{ t('sandbox.remote', { host: remoteHost }) }}
        </DouText>
      </div>
    </template>
  </DouPopover>
</template>

<style scoped>
.source {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.source-row {
  display: flex;
  align-items: center;
  gap: 8px;
}

.source-path {
  flex: 1;
  min-width: 0;
}
</style>
