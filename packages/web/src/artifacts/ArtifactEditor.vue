<script setup lang="ts">
import type { ArtifactVersionView, ArtifactView } from '@ariadne/core'
import { DouAlert, DouButton, DouInput, DouSelect, DouText } from 'dou-ui'
import { CheckIcon, ChevronLeftIcon, PencilIcon, RotateCcwIcon, SaveIcon } from '@dou-ui/icons'
import { computed, defineAsyncComponent, nextTick, ref, watch } from 'vue'
import { api, ApiError } from '../api'
import { locale, t, tm } from '../i18n'
import { openMain, report, state } from '../store'
import { authorName } from './author'
import { kindIcon } from './kinds'

const props = defineProps<{ id: string }>()

// Each kind has its own dou-ui package; load only the one in use.
const bodies = {
  document: defineAsyncComponent(() => import('./DocumentBody.vue')),
  whiteboard: defineAsyncComponent(() => import('./WhiteboardBody.vue')),
  dashboard: defineAsyncComponent(() => import('./DashboardBody.vue')),
}

const artifact = ref<ArtifactView | null>(null)
const versions = ref<ArtifactVersionView[]>([])
/** The newest version; an older one is shown read-only. */
const latest = ref(0)
/** Content edited and not saved yet; `null` when nothing changed. */
const draft = ref<string | null>(null)
const saving = ref(false)
const conflict = ref<string | null>(null)
const renaming = ref(false)
const title = ref('')
const titleInput = ref<InstanceType<typeof DouInput> | null>(null)

const viewingOld = computed(() => !!artifact.value && artifact.value.version !== latest.value)
const dirty = computed(() => !viewingOld.value && draft.value !== null)

const versionOptions = computed(() =>
  versions.value.map((v) => ({
    value: v.version,
    label: `${t('artifact.version', { version: v.version })} · ${authorName(v.author)}${v.reason ? ` · ${v.reason}` : ''}`,
  })),
)

function show(next: ArtifactView) {
  artifact.value = next
  draft.value = null
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
  () => {
    renaming.value = false
    void load()
  },
  { immediate: true },
)
// Someone else saved (an agent, or this user in another tab): reload unless edits are open.
watch(
  () => state.artifacts.find((a) => a.id === props.id)?.version,
  (version) => {
    if (version && version !== latest.value && !dirty.value && !viewingOld.value) void load()
  },
)

async function save(
  input: { content?: string; title?: string; reason?: string } = {},
): Promise<boolean> {
  const current = artifact.value
  if (!current) return false
  saving.value = true
  conflict.value = null
  try {
    const detail = await api.saveArtifact(props.id, {
      content: input.content ?? draft.value ?? current.content,
      baseVersion: latest.value,
      reason: input.reason ?? t('artifact.saveReason'),
      ...(input.title ? { title: input.title } : {}),
    })
    versions.value = detail.versions
    latest.value = detail.artifact.version
    show(detail.artifact)
    return true
  } catch (error) {
    if (error instanceof ApiError && error.status === 409) conflict.value = tm(error.localized)
    // The reason is written for the agent; the user gets a plain line.
    else if (error instanceof ApiError && error.localized.key === 'error.artifactInvalid') {
      state.error = t('artifact.invalidSave')
    } else report(error)
    return false
  } finally {
    saving.value = false
  }
}

function restore() {
  if (!artifact.value) return
  void save({
    content: artifact.value.content,
    reason: t('artifact.restoreReason', { version: artifact.value.version }),
  })
}

async function startRename() {
  title.value = artifact.value?.title ?? ''
  renaming.value = true
  await nextTick()
  titleInput.value?.focus()
}

async function rename() {
  const next = title.value.trim()
  if (!next || next === artifact.value?.title) {
    renaming.value = false
    return
  }
  // A rename carries no unsaved edits with it: those stay a draft until saved.
  const kept = draft.value
  if (
    await save({
      content: artifact.value?.content,
      title: next,
      reason: t('artifact.renameReason'),
    })
  ) {
    renaming.value = false
    draft.value = kept
  }
}

const time = (at: number) =>
  new Date(at).toLocaleString(locale.value, { dateStyle: 'short', timeStyle: 'short' })
</script>

<template>
  <div class="artifact">
    <div class="artifact-header">
      <DouButton variant="ghost" size="sm" @click="openMain({ kind: 'artifacts' })">
        <template #leading><ChevronLeftIcon /></template>
        {{ t('artifact.back') }}
      </DouButton>
      <component :is="kindIcon[artifact.kind]" v-if="artifact" class="artifact-kind" />
      <form v-if="renaming" class="artifact-title" @submit.prevent="rename">
        <DouInput
          ref="titleInput"
          v-model="title"
          size="sm"
          :aria-label="t('artifact.rename')"
          @keydown.esc="renaming = false"
        />
        <DouButton
          size="sm"
          variant="ghost"
          native-type="submit"
          :aria-label="t('artifact.rename')"
          :loading="saving"
        >
          <CheckIcon />
        </DouButton>
      </form>
      <div v-else class="artifact-title">
        <DouText weight="medium" truncate>{{ artifact?.title }}</DouText>
        <DouButton
          v-if="artifact && !viewingOld"
          size="sm"
          variant="ghost"
          :aria-label="t('artifact.rename')"
          :title="t('artifact.rename')"
          @click="startRename"
        >
          <PencilIcon />
        </DouButton>
      </div>
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
        :loading="saving && !renaming"
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
      <component
        :is="bodies[artifact.kind]"
        v-if="artifact"
        :content="artifact.content"
        :project-id="artifact.projectId"
        :artifact-id="artifact.id"
        :readonly="viewingOld"
        :label="artifact.title"
        @draft="(content: string | null) => (draft = content)"
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

.artifact-kind {
  flex: none;
  color: var(--dou-color-text-tertiary);
}

.artifact-title {
  display: flex;
  flex: 1;
  align-items: center;
  gap: 4px;
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
}
</style>
