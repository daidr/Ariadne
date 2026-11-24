<script setup lang="ts">
import type { ProjectView } from '@ariadne/core'
import { DouBadge, DouSpinner } from 'dou-ui'
import { FolderIcon } from '@dou-ui/icons'
import { onBeforeUnmount } from 'vue'
import { t } from '../i18n'
import ProjectActivityPopover from './ProjectActivityPopover.vue'

const props = defineProps<{
  project: ProjectView
  current: boolean
  canReorder: boolean
  inspecting: boolean
  activityDisabled: boolean
}>()

const emit = defineEmits<{
  select: [id: string]
  inspect: [id: string, open: boolean]
  move: [event: KeyboardEvent, id: string, offset: number]
}>()

function onSelect(dismiss: () => void) {
  dismiss()
  emit('select', props.project.id)
}

onBeforeUnmount(() => {
  if (props.inspecting) emit('inspect', props.project.id, false)
})
</script>

<template>
  <ProjectActivityPopover
    v-slot="{ inspect, dismiss }"
    :project-id="project.id"
    :name="project.name"
    :open="inspecting"
    :disabled="activityDisabled"
    @update:open="(open) => emit('inspect', project.id, open)"
  >
    <button
      type="button"
      class="projects-open"
      :data-project-id="project.id"
      :data-dou-sortable-handle="canReorder ? '' : undefined"
      :aria-current="current ? 'page' : undefined"
      :aria-expanded="activityDisabled ? undefined : inspecting"
      :aria-keyshortcuts="canReorder ? 'Alt+ArrowUp Alt+ArrowDown' : undefined"
      @click="onSelect(dismiss)"
      @focus="inspect"
      @blur="dismiss"
      @keydown.esc="dismiss"
      @keydown.enter.space.stop
      @keydown.alt.up.prevent="emit('move', $event, project.id, -1)"
      @keydown.alt.down.prevent="emit('move', $event, project.id, 1)"
    >
      <span class="projects-folder">
        <FolderIcon aria-hidden="true" />
        <span v-if="project.runningTurns" class="projects-running">
          <DouSpinner :size="8" :label="t('project.running', { count: project.runningTurns })" />
        </span>
      </span>
      <span class="projects-name">{{ project.name }}</span>
      <DouBadge class="projects-count" :value="project.needsMe" tone="warning" />
    </button>
  </ProjectActivityPopover>
</template>

<style scoped>
.projects-open {
  --project-row-bg: var(--project-row-base);
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  min-height: var(--project-row-min-height, 32px);
  padding: 0 10px;
  border: 0;
  border-radius: var(--dou-radius-control);
  background: var(--project-row-bg);
  color: var(--dou-color-text-secondary);
  font: inherit;
  text-align: start;
  cursor: pointer;
}

.projects-open[data-dou-sortable-handle] {
  cursor: grab;
}

.projects-open:hover {
  --project-row-bg: color-mix(in oklab, var(--dou-color-text) 6%, var(--project-row-base));
  color: var(--dou-color-text);
}

.projects-open[aria-current] {
  --project-row-bg: color-mix(in oklab, var(--dou-color-text) 8%, var(--project-row-base));
  color: var(--dou-color-text);
  font-weight: var(--dou-font-weight-medium);
}

.projects-open:focus-visible {
  outline: 2px solid var(--dou-color-focus);
  outline-offset: -2px;
}

.projects-folder {
  position: relative;
  display: grid;
  place-items: center;
  flex: none;
  width: 16px;
  height: 16px;
  font-size: 16px;
}

.projects-running {
  position: absolute;
  right: -3px;
  bottom: -2px;
  display: flex;
  padding: 1px;
  border-radius: 50%;
  background: var(--project-row-bg);
  color: var(--dou-color-primary-text);
}

@media (prefers-reduced-motion: reduce) {
  .projects-running :deep(.dou-spinner) {
    animation: none;
  }
}

.projects-name {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.projects-count {
  flex: none;
}
</style>
