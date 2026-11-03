<script setup lang="ts">
import type { ArtifactVersionView, ArtifactView } from '@ariadne/core'
import { DouEditor, editorKit, type EditorDocument, type PartialBlock } from '@dou-ui/editor'
import { blocksToMarkdown, markdownToBlocks } from '@dou-ui/editor/markdown'
import '@dou-ui/editor/style.css'
import { DouAlert, DouButton, DouSelect, DouText } from 'dou-ui'
import { ChevronLeftIcon, RotateCcwIcon, SaveIcon } from '@dou-ui/icons'
import { computed, ref, shallowRef, watch } from 'vue'
import { api, ApiError } from '../api'
import { locale, t, tm } from '../i18n'
import { openMain, report, state } from '../store'
import { authorName } from './author'

const props = defineProps<{ id: string }>()

const extensions = [editorKit()]
const artifact = ref<ArtifactView | null>(null)
const versions = ref<ArtifactVersionView[]>([])
/** The newest version; an older one is shown read-only. */
const latest = ref(0)
const doc = shallowRef<EditorDocument<PartialBlock>>({ version: 1, blocks: [] })
/** Markdown of the document as loaded, written the way saving would write it. */
const baseline = ref('')
const saving = ref(false)
const conflict = ref<string | null>(null)
// Bumped to remount the editor when content is replaced from outside.
const generation = ref(0)

const viewingOld = computed(() => !!artifact.value && artifact.value.version !== latest.value)
const current = computed(() => blocksToMarkdown(doc.value.blocks as never, extensions))
const dirty = computed(() => !viewingOld.value && current.value !== baseline.value)

const versionOptions = computed(() =>
  versions.value.map((v) => ({
    value: v.version,
    label: `${t('artifact.version', { version: v.version })} · ${authorName(v.author)}${v.reason ? ` · ${v.reason}` : ''}`,
  })),
)

function show(next: ArtifactView) {
  artifact.value = next
  const blocks = markdownToBlocks(next.content, extensions)
  doc.value = { version: 1, blocks }
  baseline.value = blocksToMarkdown(blocks, extensions)
  generation.value++
}

async function load(version?: number) {
  try {
    const detail = await api.getArtifact(props.id, version)
    versions.value = detail.versions
    latest.value = detail.versions[0]?.version ?? detail.artifact.version
    show(detail.artifact)
    conflict.value = null
  } catch (error) {
    report(error)
  }
}

watch(
  () => props.id,
  () => void load(),
  { immediate: true },
)
// Someone else saved (an agent, or this user in another tab): reload unless edits are open.
watch(
  () => state.artifacts.find((a) => a.id === props.id)?.version,
  (version) => {
    if (version && version !== latest.value && !dirty.value && !viewingOld.value) void load()
  },
)

async function save(content = current.value, reason = t('artifact.saveReason')) {
  if (!artifact.value) return
  saving.value = true
  conflict.value = null
  try {
    const detail = await api.saveArtifact(props.id, { content, baseVersion: latest.value, reason })
    versions.value = detail.versions
    latest.value = detail.artifact.version
    show(detail.artifact)
  } catch (error) {
    if (error instanceof ApiError && error.status === 409) conflict.value = tm(error.localized)
    else report(error)
  } finally {
    saving.value = false
  }
}

function restore() {
  if (!artifact.value) return
  void save(
    artifact.value.content,
    t('artifact.restoreReason', { version: artifact.value.version }),
  )
}

const time = (at: number) =>
  new Date(at).toLocaleString(locale.value, { dateStyle: 'short', timeStyle: 'short' })
</script>

<template>
  <div class="artifact">
    <div class="artifact-header">
      <DouButton variant="ghost" size="sm" @click="openMain({ kind: 'conversation' })">
        <template #leading><ChevronLeftIcon /></template>
        {{ t('artifact.back') }}
      </DouButton>
      <DouText weight="medium" truncate class="artifact-title">{{ artifact?.title }}</DouText>
      <DouText v-if="artifact" variant="tertiary" size="sm" class="artifact-meta">
        {{
          t('artifact.updated', {
            who: authorName(artifact.updatedBy),
            time: time(artifact.updatedAt),
          })
        }}
      </DouText>
      <div v-if="versionOptions.length > 1" class="artifact-versions">
        <DouSelect
          :model-value="artifact?.version ?? null"
          :options="versionOptions"
          size="sm"
          :aria-label="t('artifact.versions')"
          @update:model-value="(value) => load(Number(value))"
        />
      </div>
      <DouButton v-if="viewingOld" size="sm" variant="outline" :loading="saving" @click="restore">
        <template #leading><RotateCcwIcon /></template>
        {{ t('artifact.restore') }}
      </DouButton>
      <DouButton
        v-else
        size="sm"
        tone="primary"
        :disabled="!dirty"
        :loading="saving"
        @click="save()"
      >
        <template #leading><SaveIcon /></template>
        {{ t('artifact.save') }}
      </DouButton>
    </div>
    <DouAlert v-if="viewingOld" tone="neutral" class="artifact-alert">
      {{ t('artifact.viewing', { version: artifact?.version ?? 0 }) }}
    </DouAlert>
    <DouAlert v-else-if="conflict" tone="warning" class="artifact-alert">
      {{ conflict }}
    </DouAlert>
    <DouText v-else-if="dirty" variant="tertiary" size="sm" class="artifact-dirty">
      {{ t('artifact.unsaved') }}
    </DouText>
    <div class="artifact-body">
      <DouEditor
        :key="generation"
        v-model="doc"
        :readonly="viewingOld"
        :extensions="extensions"
        :aria-label="artifact?.title"
      />
    </div>
  </div>
</template>

<style scoped>
.artifact {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
}

.artifact-header {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 16px;
  border-bottom: 1px solid var(--dou-color-border);
}

.artifact-title {
  flex: 1;
  min-width: 0;
}

.artifact-meta {
  flex: none;
  white-space: nowrap;
}

.artifact-versions {
  flex: none;
  width: 200px;
}

.artifact-alert {
  margin: 8px 16px 0;
}

.artifact-dirty {
  padding: 4px 16px 0;
}

.artifact-body {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: 16px 24px;
}
</style>
