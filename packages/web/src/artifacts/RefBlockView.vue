<script lang="ts">
import type { ArtifactView } from '@ariadne/core'
import { api } from '../api'

// Shared bounded cache keyed by artifactId@version so multiple cards reuse one RPC.
const detailCache = new Map<string, Promise<ArtifactView | null>>()

function fetchReferencedArtifact(
  id: string,
  version: number | undefined,
): Promise<ArtifactView | null> {
  const key = `${id}@${version ?? 0}`
  const hit = detailCache.get(key)
  if (hit) return hit
  if (detailCache.size >= 64) {
    const oldest = detailCache.keys().next().value
    if (oldest !== undefined) detailCache.delete(oldest)
  }
  const pending = api
    .getArtifact(id, { versions: false })
    .then((res) => res.artifact)
    .catch(() => {
      if (detailCache.get(key) === pending) detailCache.delete(key)
      return null
    })
  detailCache.set(key, pending)
  return pending
}
</script>

<script setup lang="ts">
import type { BlockViewProps } from '@dou-ui/editor'
import type { FlowDocument, FlowNode } from '@dou-ui/flow'
import { ArrowUpRightIcon, LinkIcon } from '@dou-ui/icons'
import { DouButton, DouTag, DouText } from 'dou-ui'
import { computed, ref, watch } from 'vue'
import { list, t } from '../i18n'
import { openView, state } from '../store'
import { requestArtifactFocus } from './artifact-focus'
import { groupOptionsMap, type DecisionGroupData } from './decision-groups'
import { kindIcon } from './kinds'
import type { OptionNodeData } from './option-node'
import { normalizeRefProps } from './document-block-data'

const props = defineProps<BlockViewProps>()

const refProps = computed(() => normalizeRefProps(props.block.props))

const summary = computed(() => {
  const id = refProps.value?.artifactId
  if (!id) return null
  return state.artifacts.find((a) => a.id === id) ?? null
})

const detail = ref<ArtifactView | null>(null)
const loading = ref(false)
const missing = ref(false)
let loadSeq = 0

watch(
  () => ({
    id: refProps.value?.artifactId ?? '',
    version: summary.value?.version,
    updatedAt: summary.value?.updatedAt,
    needsBoard: !!(refProps.value?.groupId || refProps.value?.optionId),
  }),
  async ({ id, version, needsBoard }) => {
    const seq = ++loadSeq
    if (!id) {
      loading.value = false
      detail.value = null
      missing.value = true
      return
    }
    // Always fetch when targeting a board group/option or when the summary is not in store yet.
    if (!needsBoard && summary.value) {
      loading.value = false
      detail.value = null
      missing.value = false
      return
    }
    loading.value = true
    missing.value = false
    const loaded = await fetchReferencedArtifact(id, version)
    if (seq !== loadSeq) return
    loading.value = false
    if (!loaded) {
      detail.value = null
      missing.value = !summary.value
      return
    }
    detail.value = loaded
  },
  { immediate: true },
)

interface BoardPreview {
  title: string
  subtitle?: string
  statusText?: string
  statusTone?: 'primary' | 'success' | 'danger' | 'neutral'
  targetNodeId?: string
}

const boardPreview = computed<BoardPreview | null>(() => {
  const r = refProps.value
  const art = detail.value
  if (!r || !art || art.kind !== 'whiteboard') return null
  let doc: FlowDocument
  try {
    doc = JSON.parse(art.content) as FlowDocument
  } catch {
    return null
  }
  if (!Array.isArray(doc.nodes)) return null
  const { byId, optionsByGroup } = groupOptionsMap(doc)

  if (r.optionId) {
    const opt = byId.get(r.optionId)
    if (!opt) return null
    const data = (opt.data ?? {}) as OptionNodeData
    const groupNode = opt.parent ? byId.get(opt.parent) : undefined
    const groupLabel = (groupNode?.data as DecisionGroupData | undefined)?.label
    const statusText = data.chosen ? t('artifact.ref.chosen') : t('artifact.ref.unpicked')
    const statusTone: BoardPreview['statusTone'] = data.chosen ? 'primary' : 'neutral'
    return {
      title: data.title || r.label || art.title,
      subtitle: [art.title, groupLabel, data.note].filter(Boolean).join(' · '),
      statusText,
      statusTone,
      targetNodeId: opt.id,
    }
  }

  if (r.groupId) {
    const group: FlowNode | undefined = byId.get(r.groupId)
    if (!group) return null
    const data = (group.data ?? {}) as DecisionGroupData
    const options = optionsByGroup.get(group.id) ?? []
    const chosenTitles = options
      .filter((opt) => (opt.data as OptionNodeData | undefined)?.chosen)
      .map((opt) => (opt.data as OptionNodeData | undefined)?.title)
      .filter((title): title is string => !!title)
    const subtitleDetail = chosenTitles.length > 0 ? list(chosenTitles) : ''
    const statusText = data.confirmed
      ? t('artifact.group.confirmed')
      : t('artifact.ref.groupProgress', {
          picked: chosenTitles.length,
          total: options.length,
        })
    const statusTone: BoardPreview['statusTone'] =
      !data.confirmed && chosenTitles.length > 0 ? 'primary' : 'neutral'
    return {
      title: data.label || r.label || art.title,
      subtitle: [art.title, subtitleDetail || data.note].filter(Boolean).join(' · '),
      statusText,
      statusTone,
      targetNodeId: group.id,
    }
  }

  return null
})

const displayKind = computed(() => summary.value?.kind ?? detail.value?.kind ?? null)
const displayTitle = computed(
  () =>
    boardPreview.value?.title ||
    refProps.value?.label ||
    summary.value?.title ||
    detail.value?.title ||
    refProps.value?.artifactId ||
    '',
)
const displayVersion = computed(() => summary.value?.version ?? detail.value?.version)
const displaySubtitle = computed(() => {
  if (boardPreview.value?.subtitle) return boardPreview.value.subtitle
  if (missing.value) return t('artifact.ref.missing')
  if (loading.value) return t('artifact.ref.loading')
  if (displayVersion.value) return t('artifact.ref.version', { version: displayVersion.value })
  return ''
})

function openReferenced() {
  const r = refProps.value
  if (!r?.artifactId || missing.value) return
  const targetNodeId = boardPreview.value?.targetNodeId || r.optionId || r.groupId
  if (targetNodeId) {
    requestArtifactFocus(r.artifactId, targetNodeId)
  }
  void openView({ kind: 'artifact', id: r.artifactId })
}
</script>

<template>
  <div
    v-if="refProps"
    class="doc-ref"
    :class="{ 'is-missing': missing, 'is-selected': selected }"
    contenteditable="false"
  >
    <span class="doc-ref-icon" :class="displayKind ? `is-${displayKind}` : undefined">
      <component :is="displayKind ? kindIcon[displayKind] : LinkIcon" />
    </span>
    <span class="doc-ref-body">
      <span class="doc-ref-row">
        <DouText weight="medium" size="sm" truncate>{{ displayTitle }}</DouText>
        <DouTag
          v-if="boardPreview?.statusText"
          size="sm"
          :tone="boardPreview.statusTone ?? 'neutral'"
        >
          {{ boardPreview.statusText }}
        </DouTag>
        <DouTag v-else-if="displayVersion && !missing" size="sm" tone="neutral">
          {{ t('artifact.ref.version', { version: displayVersion }) }}
        </DouTag>
      </span>
      <DouText v-if="displaySubtitle" variant="tertiary" size="sm" truncate>
        {{ displaySubtitle }}
      </DouText>
    </span>
    <DouButton
      v-if="!missing"
      size="sm"
      variant="ghost"
      class="doc-ref-action"
      @click.stop="openReferenced"
    >
      {{
        refProps.optionId || refProps.groupId ? t('artifact.ref.locate') : t('artifact.ref.open')
      }}
      <template #trailing><ArrowUpRightIcon /></template>
    </DouButton>
  </div>
</template>

<style scoped>
.doc-ref {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 12px;
  margin: 4px 0;
  border: 1px solid var(--dou-color-border);
  border-radius: var(--dou-radius-md, 10px);
  background: var(--dou-color-surface-subtle, var(--dou-color-surface));
  transition: border-color 0.15s ease;
}

.doc-ref:hover:not(.is-missing) {
  border-color: color-mix(in srgb, var(--dou-color-primary) 45%, var(--dou-color-border));
}

.doc-ref.is-missing {
  opacity: 0.7;
}

.doc-ref.is-selected {
  outline: 2px solid color-mix(in srgb, var(--dou-color-primary) 45%, transparent);
  outline-offset: 1px;
}

.doc-ref-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: none;
  width: 30px;
  height: 30px;
  border-radius: 8px;
  background: var(--dou-color-primary-soft);
  color: var(--dou-color-primary-text);
}

.doc-ref-icon.is-whiteboard {
  background: var(--dou-color-warning-soft);
  color: var(--dou-color-warning-text);
}

.doc-ref-icon.is-dashboard {
  background: var(--dou-color-success-soft);
  color: var(--dou-color-success-text);
}

.doc-ref-body {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
  flex: 1;
}

.doc-ref-row {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}

.doc-ref-action {
  flex: none;
}
</style>
