<script setup lang="ts">
import { DouAlert, DouButton, DouEmpty, DouPopconfirm, DouSpinner, DouText } from 'dou-ui'
import { TrashIcon } from '@dou-ui/icons'
import { computed, onBeforeUnmount, onMounted, ref, shallowRef } from 'vue'
import { api, ApiError, type ProjectWorktrees, type StorageSnapshot } from '../api'
import { formatBytes } from '../bytes'
import { t, tm } from '../i18n'
import { report } from '../store'
import StageLabel from './StageLabel.vue'

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
  props.storage?.total == null || projects.value?.some((project) => project.error)
    ? null
    : t(props.storage.logical ? 'data.logicalSize' : 'data.allocatedSize', {
        size: formatBytes(props.storage.total),
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
      <DouText weight="medium">{{ t('settings.tab.worktrees') }}</DouText>
      <DouSpinner v-if="!projects || measuring" size="sm" />
    </div>
    <DouAlert v-if="error" tone="danger" closable @close="error = null">{{ error }}</DouAlert>
    <DouEmpty v-if="projects && !projects.length" size="sm" :description="t('worktrees.empty')" />
    <template v-else-if="projects">
      <DouText
        v-if="total"
        size="sm"
        variant="secondary"
        :title="storage?.logical ? undefined : t('data.allocatedHint')"
      >
        {{ total }}
      </DouText>
      <section v-for="project in projects" :key="project.id" class="worktrees-project">
        <div class="worktrees-head">
          <DouText weight="medium" size="sm">{{ project.name }}</DouText>
        </div>
        <DouText v-if="project.error" size="sm" tone="danger">{{ tm(project.error) }}</DouText>
        <ul class="worktrees-list">
          <li v-for="worktree in project.worktrees" :key="worktree.path" class="worktree">
            <div class="worktree-main">
              <div class="worktree-title">
                <DouText size="sm" truncate>
                  {{ worktree.thread?.title ?? t('worktrees.unknown') }}
                </DouText>
                <DouText v-if="worktree.orphaned" size="sm" tone="warning" class="worktree-size">
                  {{ t('worktrees.orphaned') }}
                </DouText>
                <DouText v-else-if="worktree.thread?.merged" size="sm" tone="success">
                  {{ t('worktrees.merged') }}
                </DouText>
                <StageLabel v-else-if="worktree.thread" :stage="worktree.thread.stage" />
              </div>
              <DouText size="sm" variant="tertiary" truncate :title="worktree.path">
                {{ worktree.path }}
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
            <DouPopconfirm
              :title="
                t(worktree.orphaned ? 'worktrees.removeOrphanConfirm' : 'worktrees.removeConfirm', {
                  title: worktree.thread?.title ?? worktree.threadId,
                })
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
              @confirm="remove(project.id, worktree)"
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
}

.worktrees-heading {
  display: flex;
  align-items: center;
  gap: 8px;
}

.worktrees-project {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.worktrees-head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 4px;
}

.worktrees-list {
  display: flex;
  flex-direction: column;
  margin: 0;
  padding: 0;
  list-style: none;
}

.worktree {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 0;
  border-bottom: 1px solid var(--dou-color-border-subtle);
}

.worktree:last-child {
  border-bottom: none;
}

.worktree-main {
  display: flex;
  flex: 1;
  flex-direction: column;
  min-width: 0;
}

.worktree-title {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.worktree-size {
  flex: none;
  white-space: nowrap;
}
</style>
