<script setup lang="ts">
import { DouBadge, DouButton, DouEmpty, DouLink, DouPopover, DouSpinner, DouText } from 'dou-ui'
import { BellIcon, FolderIcon, PlayIcon, RotateCwIcon } from '@dou-ui/icons'
import { computed, watch } from 'vue'
import { t } from '../i18n'
import type { AttentionView, NeedsMeThreadSummary } from '@ariadne/core'
import { threadLine } from '../stage'
import {
  endAside,
  openProject,
  openProjectConversation,
  openProjectThread,
  refreshAllNeedsMe,
  report,
  retryTurn,
  state,
  threadsById,
} from '../store'
import AskBlock from '../blocks/AskBlock.vue'
import ReportArtifacts from './ReportArtifacts.vue'
import ReviewMark from '../review/ReviewMark.vue'

defineProps<{ compact?: boolean }>()

type ResolvedNeedsMeItem =
  | {
      kind: 'attention'
      projectId: string
      attention: AttentionView
      askers: NeedsMeThreadSummary[]
      at: number
    }
  | {
      kind: 'thread'
      projectId: string
      thread: NeedsMeThreadSummary
      at: number
    }

interface AttentionProjectGroup {
  projectId: string
  projectName: string
  isCurrent: boolean
  latestAt: number
  items: ResolvedNeedsMeItem[]
}

const totalCount = computed(() => {
  if (state.projects.length) {
    return state.projects.reduce((sum, project) => sum + project.needsMe, 0)
  }
  if (state.allNeedsMe) return state.allNeedsMe.items.length
  return state.detail?.needsMe.length ?? 0
})

watch(
  () => state.attentionOpen,
  (open) => {
    if (open) void refreshAllNeedsMe().catch(report)
  },
)

function retryLoad() {
  void refreshAllNeedsMe().catch(report)
}

const groups = computed<AttentionProjectGroup[]>(() => {
  const currentId = state.currentId
  const localById = threadsById.value

  if (state.allNeedsMe) {
    const byProject = new Map<string, AttentionProjectGroup>()
    const summaries = state.allNeedsMe.threads
    const resolveThread = (id: string): NeedsMeThreadSummary | undefined =>
      summaries[id] ?? localById.get(id)

    for (const entry of state.allNeedsMe.items) {
      let resolved: ResolvedNeedsMeItem | null = null
      if (entry.item.kind === 'attention') {
        const askers = [
          ...(entry.item.threadId ? [entry.item.threadId] : []),
          ...entry.item.joinedIds,
        ].flatMap((id) => {
          const thread = resolveThread(id)
          return thread ? [thread] : []
        })
        resolved = {
          kind: 'attention',
          projectId: entry.projectId,
          attention: entry.item.attention,
          askers,
          at: entry.at,
        }
      } else {
        const thread = resolveThread(entry.item.threadId)
        if (thread) {
          resolved = {
            kind: 'thread',
            projectId: entry.projectId,
            thread,
            at: entry.at,
          }
        }
      }
      if (!resolved) continue

      let group = byProject.get(entry.projectId)
      if (!group) {
        group = {
          projectId: entry.projectId,
          projectName: entry.projectName,
          isCurrent: entry.projectId === currentId,
          latestAt: entry.at,
          items: [],
        }
        byProject.set(entry.projectId, group)
      }
      group.latestAt = Math.max(group.latestAt, entry.at)
      group.items.push(resolved)
    }

    return [...byProject.values()].toSorted((a, b) => {
      if (a.isCurrent !== b.isCurrent) return a.isCurrent ? -1 : 1
      return (
        b.latestAt - a.latestAt ||
        a.projectName.localeCompare(b.projectName) ||
        a.projectId.localeCompare(b.projectId)
      )
    })
  }

  // Fallback before the global list finishes loading when only current project detail is available.
  const detail = state.detail
  if (!detail?.needsMe.length || state.allNeedsMeLoading || state.allNeedsMeError) return []
  const resolved: ResolvedNeedsMeItem[] = []
  for (const item of detail.needsMe) {
    if (item.kind === 'attention') {
      const askers = [...(item.threadId ? [item.threadId] : []), ...item.joinedIds].flatMap(
        (id) => {
          const thread = localById.get(id)
          return thread ? [thread] : []
        },
      )
      resolved.push({
        kind: 'attention',
        projectId: detail.project.id,
        attention: item.attention,
        askers,
        at: item.attention.createdAt,
      })
    } else {
      const thread = localById.get(item.threadId)
      if (thread) {
        resolved.push({
          kind: 'thread',
          projectId: detail.project.id,
          thread,
          at: thread.updatedAt,
        })
      }
    }
  }
  if (!resolved.length) return []
  return [
    {
      projectId: detail.project.id,
      projectName: detail.project.name,
      isCurrent: detail.project.id === currentId,
      latestAt: Math.max(...resolved.map((entry) => entry.at)),
      items: resolved,
    },
  ]
})

const showProjectHeaders = computed(
  () => groups.value.length > 1 || (groups.value.length === 1 && !groups.value[0]!.isCurrent),
)

const isInitialLoading = computed(
  () => state.allNeedsMe === null && state.allNeedsMeLoading && totalCount.value > 0,
)

const isInitialError = computed(
  () => state.allNeedsMe === null && state.allNeedsMeError && !state.allNeedsMeLoading,
)

function closePopover() {
  state.attentionOpen = false
}

function jumpProject(projectId: string) {
  closePopover()
  if (projectId !== state.currentId || state.starting || !state.detail) {
    void openProject(projectId)
  }
}

function jumpThread(
  event: MouseEvent | KeyboardEvent | undefined,
  projectId: string,
  threadId: string,
) {
  closePopover()
  void openProjectThread(projectId, threadId, event?.altKey ? { beside: 'horizontal' } : {})
}

/** Go to where a form or coordinator question was asked: its thread, or the project's coordinator. */
function openAsker(projectId: string, threadId: string | null) {
  closePopover()
  if (threadId) void openProjectThread(projectId, threadId)
  else void openProjectConversation(projectId)
}

/** The line the thread list shows for it; its stage when its report said nothing. */
function headline(thread: NeedsMeThreadSummary): string {
  if (thread.aside?.due) {
    return thread.aside.reason
      ? t('aside.dueReason', { reason: thread.aside.reason })
      : t('aside.due')
  }
  return threadLine(thread) ?? t(`thread.stage.${thread.stage}`)
}
</script>

<template>
  <DouPopover
    v-model:open="state.attentionOpen"
    :title="t('attention.title')"
    :width="360"
    placement="bottom-end"
  >
    <!-- With nothing waiting only the bell stays, so it takes no room from the header. -->
    <DouButton
      variant="ghost"
      size="sm"
      class="attention-trigger"
      :aria-label="t('attention.title')"
      :title="totalCount ? undefined : t('attention.title')"
    >
      <template #leading><BellIcon /></template>
      <template v-if="totalCount" #default>
        <template v-if="!compact">{{ t('attention.title') }}</template>
        <DouBadge :value="totalCount" tone="warning" />
      </template>
    </DouButton>
    <template #content>
      <div v-if="isInitialLoading" class="attention-status">
        <DouSpinner size="sm" />
      </div>
      <div v-else-if="isInitialError" class="attention-status">
        <DouText size="sm" variant="secondary">{{ t('attention.error') }}</DouText>
        <DouButton size="sm" variant="outline" @click="retryLoad">
          <template #leading><RotateCwIcon /></template>
          {{ t('conversation.retry') }}
        </DouButton>
      </div>
      <div v-else-if="groups.length" class="attention-list">
        <section
          v-for="group in groups"
          :key="group.projectId"
          class="attention-group"
          :data-attention-project-id="group.projectId"
        >
          <div v-if="showProjectHeaders" class="attention-group-header">
            <span class="attention-group-label">
              <FolderIcon class="attention-group-icon" aria-hidden="true" />
              <DouLink
                class="attention-group-name"
                underline="hover"
                :title="group.projectName"
                @click="jumpProject(group.projectId)"
              >
                {{ group.projectName }}
              </DouLink>
            </span>
            <span class="attention-group-meta">
              <DouText v-if="group.isCurrent" size="sm" variant="tertiary">
                {{ t('attention.currentProject') }}
              </DouText>
              <DouBadge :value="group.items.length" tone="warning" />
            </span>
          </div>
          <div class="attention-group-items">
            <template v-for="item in group.items">
              <div
                v-if="item.kind === 'attention'"
                :key="item.attention.id"
                class="attention"
                :data-attention-id="item.attention.id"
              >
                <DouText v-if="item.askers.length" variant="secondary" size="sm" truncate>
                  <template v-for="(thread, index) in item.askers" :key="thread.id">
                    <template v-if="index"> · </template>
                    <DouLink
                      underline="hover"
                      @click="(event: MouseEvent) => jumpThread(event, item.projectId, thread.id)"
                      >{{ thread.title }}</DouLink
                    >
                  </template>
                </DouText>
                <!-- The coordinator's own question says so, and jumps to its project when clicked. -->
                <DouText v-else variant="secondary" size="sm">
                  <DouLink underline="hover" @click="openAsker(item.projectId, null)">
                    {{ t('coordinator.title') }}
                  </DouLink>
                </DouText>
                <!-- A form is filled in where it was asked; the list only points there. -->
                <div v-if="item.attention.block.type === 'form'" class="attention-thread">
                  <DouText weight="medium" truncate>{{ item.attention.block.title }}</DouText>
                  <DouButton
                    size="sm"
                    variant="ghost"
                    @click="openAsker(item.projectId, item.attention.threadId)"
                  >
                    {{ t('block.fill') }}
                  </DouButton>
                </div>
                <AskBlock
                  v-else
                  :attention-id="item.attention.id"
                  :block="item.attention.block"
                  :answer="null"
                />
              </div>
              <div
                v-else
                :key="item.thread.id"
                class="attention"
                :data-attention-thread-id="item.thread.id"
              >
                <div class="attention-thread">
                  <!-- The title opens the thread, so a review can be looked at before it ends. -->
                  <span class="attention-name">
                    <DouLink
                      class="attention-title"
                      underline="hover"
                      :title="item.thread.title"
                      @click="
                        (event: MouseEvent) => jumpThread(event, item.projectId, item.thread.id)
                      "
                    >
                      {{ item.thread.title }}
                    </DouLink>
                    <ReviewMark :thread="item.thread" />
                  </span>
                  <DouButton
                    v-if="item.thread.stage === 'halted' || item.thread.stage === 'silent'"
                    size="sm"
                    variant="ghost"
                    @click="retryTurn(item.thread.id)"
                  >
                    <template #leading><PlayIcon /></template>
                    {{ t('conversation.continue') }}
                  </DouButton>
                  <!-- Back from being set aside: seen, and it goes. -->
                  <DouButton
                    v-else-if="item.thread.aside?.due"
                    size="sm"
                    variant="ghost"
                    @click="endAside(item.thread.id)"
                  >
                    {{ t('aside.done') }}
                  </DouButton>
                  <DouButton
                    v-else
                    size="sm"
                    variant="ghost"
                    @click="
                      (event: MouseEvent) => jumpThread(event, item.projectId, item.thread.id)
                    "
                  >
                    {{ t('thread.open') }}
                  </DouButton>
                </div>
                <DouText variant="secondary" size="sm" :truncate="2" :title="headline(item.thread)">
                  {{ headline(item.thread) }}
                </DouText>
                <ReportArtifacts
                  :artifacts="item.thread.lastArtifacts"
                  :project-id="item.projectId"
                  @open="closePopover()"
                />
              </div>
            </template>
          </div>
        </section>
      </div>
      <DouEmpty v-else size="sm" :description="t('attention.empty')" />
    </template>
  </DouPopover>
</template>

<style scoped>
.attention-list {
  display: flex;
  flex-direction: column;
  gap: 14px;
  max-height: min(70vh, 520px);
  overflow-y: auto;
}

.attention-status {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 8px;
  padding: 16px 0;
}

.attention-group {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.attention-group + .attention-group {
  padding-top: 12px;
  border-top: 1px solid var(--dou-color-border);
}

.attention-group-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 4px 8px;
  border-radius: var(--dou-radius-control);
  background: var(--dou-color-bg-muted);
}

.attention-group-label {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}

.attention-group-icon {
  flex: none;
  font-size: 14px;
  color: var(--dou-color-text-tertiary);
}

.attention-group-name {
  min-width: 0;
  overflow: hidden;
  font-size: var(--dou-font-size-sm);
  font-weight: var(--dou-font-weight-medium);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.attention-group-meta {
  display: inline-flex;
  flex: none;
  align-items: center;
  gap: 6px;
}

.attention-group-items {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.attention {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.attention + .attention {
  padding-top: 12px;
  border-top: 1px solid var(--dou-color-border-subtle);
}

.attention-thread {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.attention-name {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}

.attention-title {
  min-width: 0;
  overflow: hidden;
  font-weight: 500;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
