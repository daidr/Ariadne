<script setup lang="ts">
import {
  DouAlert,
  DouButton,
  DouEmpty,
  DouLink,
  DouPopconfirm,
  DouSpinner,
  DouText,
  DouTooltip,
} from 'dou-ui'
import { InfoIcon, TrashIcon } from '@dou-ui/icons'
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { api, ApiError, type ProjectWorktrees } from '../api'
import { formatBytes } from '../bytes'
import { t, tm } from '../i18n'
import StageLabel from './StageLabel.vue'

// Threads' worktrees on disk, project by project, to see what they take and remove some.
const projects = ref<ProjectWorktrees[] | null>(null)
const error = ref<string | null>(null)
const removing = ref<string | null>(null)
const measuring = ref(false)
let controller: AbortController | undefined
let closed = false
onBeforeUnmount(() => {
  closed = true
  controller?.abort()
})
// On Windows a Dev Drive is what lets worktrees share storage; elsewhere there is nothing to do.
const windows = ref(false)
const DEV_DRIVE = 'https://learn.microsoft.com/windows/dev-drive/'
// The hint around the words "Dev Drive", which alone are the link.
const copied = computed(() => t('worktrees.copied', { link: '\n' }).split('\n'))

const message = (cause: unknown) =>
  cause instanceof ApiError ? tm(cause.localized) : String(cause)

async function load() {
  if (closed) return
  controller?.abort()
  controller = new AbortController()
  const { signal } = controller
  measuring.value = false
  try {
    const listed = await api.listWorktrees(false, signal)
    if (signal.aborted) return
    windows.value = listed.windows
    projects.value = listed.projects.filter((project) => project.worktrees.length || project.error)
    if (projects.value.some((project) => project.worktrees.length)) {
      measuring.value = true
      void measureSizes(signal)
    }
  } catch (cause) {
    if (signal.aborted) return
    error.value = message(cause)
    projects.value = []
  }
}
onMounted(load)

async function measureSizes(signal: AbortSignal) {
  try {
    const measured = await api.listWorktrees(true, signal)
    if (!signal.aborted)
      projects.value = measured.projects.filter(
        (project) => project.worktrees.length || project.error,
      )
  } catch (cause) {
    if (!signal.aborted) error.value = message(cause)
  } finally {
    if (!signal.aborted) measuring.value = false
  }
}

const size = formatBytes

// Worktrees that share storage with their checkout take only what changed, which the
// size of their files does not tell: they are left out of the sizes.
const total = computed(() =>
  (projects.value ?? [])
    .filter((project) => !project.shared)
    .flatMap((project) => project.worktrees)
    .reduce((sum, worktree) => sum + (worktree.bytes ?? 0), 0),
)

async function remove(projectId: string, threadId: string) {
  removing.value = threadId
  error.value = null
  const interrupted = measuring.value
  controller?.abort()
  measuring.value = false
  try {
    await api.removeWorktree(projectId, threadId)
    await load()
  } catch (cause) {
    error.value = message(cause)
    if (interrupted) await load()
  } finally {
    removing.value = null
  }
}
</script>

<template>
  <div class="worktrees">
    <DouAlert v-if="error" tone="danger" closable @close="error = null">{{ error }}</DouAlert>
    <div v-if="!projects" class="worktrees-loading"><DouSpinner /></div>
    <DouEmpty v-else-if="!projects.length" size="sm" :description="t('worktrees.empty')" />
    <template v-else>
      <DouSpinner v-if="measuring" size="sm" />
      <DouText v-else-if="total" size="sm" variant="secondary">{{
        t('worktrees.total', { size: size(total) })
      }}</DouText>
      <section v-for="project in projects" :key="project.id" class="worktrees-project">
        <div class="worktrees-head">
          <DouText weight="medium" size="sm">{{ project.name }}</DouText>
          <DouTooltip v-if="project.shared" :content="t('worktrees.shared')">
            <DouButton variant="ghost" size="sm" :aria-label="t('worktrees.shared')">
              <template #leading><InfoIcon /></template>
            </DouButton>
          </DouTooltip>
          <DouTooltip v-else-if="project.shared === false && windows">
            <DouButton variant="ghost" size="sm" :aria-label="copied.join('Dev Drive')">
              <template #leading><InfoIcon /></template>
            </DouButton>
            <template #content>
              {{ copied[0] }}
              <DouLink :href="DEV_DRIVE" external>Dev Drive</DouLink>
              {{ copied[1] }}
            </template>
          </DouTooltip>
        </div>
        <DouText v-if="project.error" size="sm" tone="danger">{{ tm(project.error) }}</DouText>
        <ul class="worktrees-list">
          <li v-for="worktree in project.worktrees" :key="worktree.threadId" class="worktree">
            <div class="worktree-main">
              <div class="worktree-title">
                <DouText size="sm" truncate>
                  {{ worktree.thread?.title ?? t('worktrees.unknown') }}
                </DouText>
                <DouText v-if="worktree.thread?.merged" size="sm" tone="success">
                  {{ t('worktrees.merged') }}
                </DouText>
                <StageLabel v-else-if="worktree.thread" :stage="worktree.thread.stage" />
              </div>
              <DouText size="sm" variant="tertiary" truncate :title="worktree.path">
                {{ worktree.path }}
              </DouText>
            </div>
            <DouText
              v-if="worktree.bytes !== null && !project.shared"
              size="sm"
              variant="secondary"
              class="worktree-size"
            >
              {{ size(worktree.bytes) }}
            </DouText>
            <DouPopconfirm
              :title="
                t('worktrees.removeConfirm', {
                  title: worktree.thread?.title ?? worktree.threadId,
                })
              "
              tone="danger"
              :confirm-text="t('worktrees.remove')"
              @confirm="remove(project.id, worktree.threadId)"
            >
              <DouButton
                size="sm"
                variant="ghost"
                tone="danger"
                :loading="removing === worktree.threadId"
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
  </div>
</template>

<style scoped>
.worktrees {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.worktrees-loading {
  display: grid;
  place-items: center;
  padding: 24px 0;
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
