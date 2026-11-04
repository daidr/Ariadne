<script setup lang="ts">
import type { PlanRevisionView, PlanUndoPreview } from '@ariadne/core'
import { DouButton, DouPopconfirm, DouText } from 'dou-ui'
import { RotateCcwIcon, RotateCwIcon } from '@dou-ui/icons'
import { computed, ref, watch } from 'vue'
import { api, ApiError } from '../api'
import { t, tm } from '../i18n'
import { report, revertPlan, state } from '../store'

/**
 * Undo a revision from the plan's history, or restore one that was undone (by undoing its
 * undo). Before anything happens it shows what will change, which dependencies go along
 * and which threads stop; when later changes are in the way, it says why it cannot.
 */
const props = defineProps<{ revision: PlanRevisionView }>()

const restore = computed(() => !!props.revision.revertedBy)
/** The revision to undo: this one, or the undo that took it back. */
const target = computed(() => props.revision.revertedBy ?? props.revision.id)

const open = ref(false)
const preview = ref<PlanUndoPreview | null>(null)
const failed = ref<string | null>(null)

watch(open, async (isOpen) => {
  if (!isOpen) return
  preview.value = null
  failed.value = null
  const project = state.detail?.project
  if (!project) return
  try {
    preview.value = (await api.previewPlanUndo(project.id, target.value)).preview
  } catch (error) {
    if (error instanceof ApiError) failed.value = tm(error.localized)
    else report(error)
  }
})

const blocked = computed(
  () => failed.value ?? (preview.value?.blocked ? tm(preview.value.blocked) : null),
)
const title = computed(() => {
  if (blocked.value) return t(restore.value ? 'plan.restore.blocked' : 'plan.undo.blocked')
  return t(restore.value ? 'plan.restore.ask' : 'plan.undo.ask')
})

async function confirm() {
  if (blocked.value || !preview.value) return
  const reason = t(restore.value ? 'plan.restore.reason' : 'plan.undo.reason', {
    reason: props.revision.reason,
  })
  await revertPlan(target.value, reason)
}
</script>

<template>
  <DouPopconfirm
    v-model:open="open"
    :title="title"
    :tone="blocked ? 'neutral' : 'warning'"
    :confirm-text="blocked ? t('plan.undo.ok') : t(restore ? 'plan.restore' : 'plan.revert')"
    :width="320"
    @confirm="confirm"
  >
    <template #description>
      <DouText v-if="blocked" size="sm">{{ blocked }}</DouText>
      <template v-else-if="preview">
        <ul class="undo-lines">
          <li v-for="(line, index) in preview.changes" :key="index">{{ tm(line) }}</li>
        </ul>
        <DouText v-if="preview.threads.length" size="sm" variant="secondary">
          {{
            t('plan.undo.threads', {
              titles: preview.threads.map((thread) => thread.title).join(t('plan.reason.join')),
            })
          }}
        </DouText>
      </template>
    </template>
    <DouButton size="sm" variant="ghost">
      <template #leading>
        <RotateCwIcon v-if="restore" />
        <RotateCcwIcon v-else />
      </template>
      {{ t(restore ? 'plan.restore' : 'plan.revert') }}
    </DouButton>
  </DouPopconfirm>
</template>

<style scoped>
.undo-lines {
  margin: 0 0 4px;
  padding-left: 18px;
}
</style>
