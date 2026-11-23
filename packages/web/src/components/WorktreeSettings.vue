<script setup lang="ts">
import {
  DouAlert,
  DouButton,
  DouEmpty,
  DouPopconfirm,
  DouShimmer,
  DouSpinner,
  DouTag,
  DouText,
} from 'dou-ui'
import { TrashIcon } from '@dou-ui/icons'
import { computed, onBeforeUnmount, onMounted, ref, shallowRef } from 'vue'
import { api, ApiError, type ProjectWorktrees, type StorageSnapshot } from '../api'
import { formatBytes } from '../bytes'
import { t, tm } from '../i18n'
import { stageTone, threadStageLabel } from '../stage'
import { report } from '../store'
import { splitWorktreePaths } from './worktree-path'

// Threads' worktrees on disk, project by project, to see what they take and remove some.
const projects = ref<ProjectWorktrees[] | null>(null)
const error = ref<string | null>(null)
const removing = ref<string | null>(null)
const props = defineProps<{ storage: StorageSnapshot | null; measuring: boolean }>()
const emit = defineEmits<{ changed: [] }>()
let controller: AbortController | undefined
let reclaimRequest: AbortController | undefined
const reclaim = shallowRef<{ path: string; exclusive?: number | null }>()
let closed = false
onBeforeUnmount(() => {
  closed = true
  controller?.abort()
  reclaimRequest?.abort()
})
const message = (cause: unknown) =>
  cause instanceof ApiError ? tm(cause.localized) : String(cause)

async function load() {
  if (closed) return
  controller?.abort()
  controller = new AbortController()
  const { signal } = controller
  try {
    const listed = await api.listWorktrees(false, signal)
    if (signal.aborted) return
    projects.value = listed.projects.filter((project) => project.worktrees.length || project.error)
  } catch (cause) {
    if (signal.aborted) return
    error.value = message(cause)
    projects.value = []
  }
}
onMounted(load)

const total = computed(() =>
  props.storage?.total == null ||
  !projects.value?.length ||
  projects.value.some((project) => project.error)
    ? null
    : t(props.storage.logical ? 'data.logicalSize' : 'data.allocatedSize', {
        size: formatBytes(props.storage.total),
      }),
)

const projectGroups = computed(() =>
  (projects.value ?? []).map((project) => {
    const { commonDir, relative } = splitWorktreePaths(project.worktrees.map((item) => item.path))
    return {
      project,
      commonDir,
      items: project.worktrees.map((worktree, index) => ({
        worktree,
        displayPath: relative[index] ?? worktree.path,
      })),
    }
  }),
)

function reclaimable(path: string, showProgress = false) {
  const size = props.storage?.worktrees[path]
  if (size?.reclaimableEstimate != null) {
    return t('worktrees.reclaimEstimate', { size: formatBytes(size.reclaimableEstimate) })
  }
  const probe = reclaim.value?.path === path ? reclaim.value : undefined
  if (showProgress && probe && probe.exclusive === undefined) {
    return t('worktrees.reclaimMeasuring')
  }
  const exclusive = probe?.exclusive ?? size?.exclusive
  return exclusive != null
    ? t('worktrees.reclaimable', { size: formatBytes(exclusive) })
    : t('worktrees.reclaimUnknown')
}

async function estimateReclaimable(path: string, open: boolean) {
  if (!open) {
    if (reclaim.value?.path === path && reclaim.value.exclusive === undefined) {
      reclaimRequest?.abort()
      reclaim.value = undefined
    }
    return
  }
  reclaimRequest?.abort()
  const size = props.storage?.worktrees[path]
  if (size?.reclaimableEstimate != null || size?.exclusive != null) return
  const request = new AbortController()
  reclaimRequest = request
  reclaim.value = { path }
  try {
    const { exclusive } = await api.measureReclaimable(path, request.signal)
    if (!request.signal.aborted) reclaim.value = { path, exclusive }
  } catch {
    if (!request.signal.aborted) reclaim.value = { path, exclusive: null }
  }
}

async function remove(projectId: string, worktree: ProjectWorktrees['worktrees'][number]) {
  const { path, threadId, sourceId, orphaned } = worktree
  removing.value = path
  if (reclaim.value?.path === path) {
    reclaimRequest?.abort()
    reclaim.value = undefined
  }
  error.value = null
  try {
    await api.removeWorktree(projectId, threadId, sourceId, orphaned)
    emit('changed')
    await load()
  } catch (cause) {
    report(cause)
    if (cause instanceof ApiError && cause.localized.key === 'error.worktreeOrphanChanged') {
      await load()
    }
  } finally {
    removing.value = null
  }
}
</script>

<template>
  <section class="worktrees">
    <div class="worktrees-heading">
      <div class="worktrees-heading-title">
        <DouText weight="medium">{{ t('settings.tab.worktrees') }}</DouText>
        <DouSpinner v-if="!projects || measuring" size="sm" />
      </div>
      <DouText
        v-if="total"
        size="sm"
        variant="secondary"
        class="worktrees-total"
        :title="storage?.logical ? undefined : t('data.allocatedHint')"
      >
        {{ total }}
      </DouText>
    </div>
    <DouAlert v-if="error" tone="danger" closable @close="error = null">{{ error }}</DouAlert>
    <DouEmpty v-if="projects && !projects.length" size="sm" :description="t('worktrees.empty')" />
    <template v-else-if="projects">
      <section v-for="group in projectGroups" :key="group.project.id" class="worktrees-project">
        <div class="worktrees-head">
          <DouText weight="medium" size="sm">{{ group.project.name }}</DouText>
          <DouText
            v-if="group.commonDir"
            size="sm"
            variant="tertiary"
            truncate
            class="worktrees-dir"
            :title="group.commonDir"
          >
            {{ group.commonDir }}
          </DouText>
        </div>
        <DouText v-if="group.project.error" size="sm" tone="danger">
          {{ tm(group.project.error) }}
        </DouText>
        <ul v-if="group.items.length" class="worktrees-list">
          <li
            v-for="{ worktree, displayPath } in group.items"
            :key="worktree.path"
            class="worktree"
            :class="{ 'worktree--has-size': storage?.worktrees[worktree.path]?.bytes != null }"
          >
            <DouText
              size="sm"
              weight="medium"
              truncate
              class="worktree-title"
              :title="worktree.thread?.title ?? t('worktrees.unknown')"
            >
              {{ worktree.thread?.title ?? t('worktrees.unknown') }}
            </DouText>
            <div class="worktree-meta">
              <DouTag v-if="worktree.orphaned" size="sm" tone="warning" class="worktree-status">
                {{ t('worktrees.orphaned') }}
              </DouTag>
              <DouTag
                v-else-if="worktree.thread?.merged"
                size="sm"
                tone="success"
                class="worktree-status"
              >
                {{ t('worktrees.merged') }}
              </DouTag>
              <DouTag
                v-else-if="worktree.thread"
                size="sm"
                :tone="
                  worktree.thread.stage === 'working' ? 'neutral' : stageTone[worktree.thread.stage]
                "
                class="worktree-status"
              >
                <DouShimmer v-if="worktree.thread.stage === 'working'">
                  {{ t('thread.stage.working') }}
                </DouShimmer>
                <template v-else>
                  {{ threadStageLabel(worktree.thread.stage, worktree.thread.pullRequest?.state) }}
                </template>
              </DouTag>
              <DouText
                size="sm"
                variant="tertiary"
                truncate
                class="worktree-path"
                :title="worktree.path"
              >
                {{ displayPath }}
              </DouText>
            </div>
            <DouText
              v-if="storage?.worktrees[worktree.path]?.bytes != null"
              size="sm"
              variant="secondary"
              class="worktree-size"
              :title="reclaimable(worktree.path)"
            >
              {{ formatBytes(storage.worktrees[worktree.path]!.bytes!) }}
            </DouText>
            <div class="worktree-action">
              <DouPopconfirm
                :title="
                  t(
                    worktree.orphaned ? 'worktrees.removeOrphanConfirm' : 'worktrees.removeConfirm',
                    {
                      title: worktree.thread?.title ?? worktree.threadId,
                    },
                  )
                "
                tone="danger"
                :width="320"
                :description="
                  worktree.orphaned
                    ? t('worktrees.removeOrphanDescription', {
                        space: reclaimable(worktree.path, true),
                      })
                    : reclaimable(worktree.path, true)
                "
                :confirm-text="t('worktrees.remove')"
                @update:open="estimateReclaimable(worktree.path, $event)"
                @confirm="remove(group.project.id, worktree)"
              >
                <DouButton
                  size="sm"
                  variant="ghost"
                  tone="danger"
                  :loading="removing === worktree.path"
                  :aria-label="t('worktrees.remove')"
                  :title="t('worktrees.remove')"
                >
                  <TrashIcon />
                </DouButton>
              </DouPopconfirm>
            </div>
          </li>
        </ul>
      </section>
    </template>
  </section>
</template>

<style scoped>
.worktrees {
  display: flex;
  flex-direction: column;
  gap: 12px;
  container-type: inline-size;
}

.worktrees-heading {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  justify-content: space-between;
  gap: 4px 12px;
}

.worktrees-heading-title {
  display: flex;
  align-items: center;
  gap: 8px;
}

.worktrees-total {
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}

.worktrees-project {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.worktrees-head {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.worktrees-dir {
  display: block;
  min-width: 0;
  font-family: var(--dou-font-family-mono);
  font-size: calc(var(--dou-font-size-sm) - 1px);
}

.worktrees-list {
  display: flex;
  flex-direction: column;
  margin: 0;
  padding: 0;
  list-style: none;
  border: 1px solid var(--dou-color-border);
  border-radius: var(--dou-radius-container);
  background: var(--dou-color-bg-surface);
  overflow: hidden;
}

.worktree {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  grid-template-rows: auto auto;
  grid-template-areas:
    'title action'
    'meta  action';
  align-items: center;
  column-gap: 10px;
  row-gap: 4px;
  padding: 10px 12px;
}

.worktree--has-size {
  grid-template-columns: minmax(0, 1fr) auto auto;
  grid-template-areas:
    'title size action'
    'meta  size action';
}

.worktree + .worktree {
  border-top: 1px solid var(--dou-color-border-subtle);
}

.worktree-title {
  grid-area: title;
  display: block;
  min-width: 0;
}

.worktree-meta {
  grid-area: meta;
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.worktree-status {
  flex: none;
}

.worktree-path {
  flex: 1;
  min-width: 0;
  font-family: var(--dou-font-family-mono);
  font-size: calc(var(--dou-font-size-sm) - 1px);
}

.worktree-size {
  grid-area: size;
  justify-self: end;
  white-space: nowrap;
  font-variant-numeric: tabular-nums;
}

.worktree-action {
  grid-area: action;
  display: flex;
  align-items: center;
}

@container (max-width: 459px) {
  .worktree {
    column-gap: 8px;
    padding: 10px;
  }

  .worktree--has-size {
    grid-template-columns: minmax(0, 1fr) auto auto;
    grid-template-areas:
      'title title action'
      'meta  size  action';
  }
}
</style>
