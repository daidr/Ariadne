<script setup lang="ts">
import type { BlockViewProps } from '@dou-ui/editor'
import {
  CheckCircleIcon,
  CheckIcon,
  ChevronDownIcon,
  ChevronRightIcon,
  HelpCircleIcon,
} from '@dou-ui/icons'
import { DouButton, DouInput, DouTag, DouText } from 'dou-ui'
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { list, t } from '../i18n'
import { normalizeAskProps, type AskBlockOption } from './document-block-data'

const props = defineProps<BlockViewProps>()

const ask = computed(() => normalizeAskProps(props.block.props))
const expanded = ref(false)

// Confirmed blocks collapse by default to keep long documents clean; unconfirmed blocks stay open.
watch(
  () => ask.value?.confirmed,
  (confirmed) => {
    expanded.value = !confirmed
  },
  { immediate: true },
)

const noteDraft = ref('')
let noteTimer: ReturnType<typeof setTimeout> | null = null

watch(
  () => ask.value?.note ?? '',
  (next) => {
    if (next !== noteDraft.value) noteDraft.value = next
  },
  { immediate: true },
)

function flushNote() {
  if (noteTimer) {
    clearTimeout(noteTimer)
    noteTimer = null
  }
  if (!props.editable || !ask.value) return
  if (noteDraft.value !== ask.value.note) {
    props.updateProps({ note: noteDraft.value })
  }
}

function onNoteInput(value: string) {
  noteDraft.value = value
  if (!props.editable) return
  if (noteTimer) clearTimeout(noteTimer)
  noteTimer = setTimeout(flushNote, 150)
}

onBeforeUnmount(flushNote)

const selectedSet = computed(() => new Set(ask.value?.selected ?? []))

function toggleOption(option: AskBlockOption) {
  if (!props.editable || !ask.value) return
  const current = ask.value.selected
  let next: string[]
  if (ask.value.pick === 'many') {
    next = selectedSet.value.has(option.id)
      ? current.filter((id) => id !== option.id)
      : [...current, option.id]
  } else {
    next = selectedSet.value.has(option.id) ? [] : [option.id]
  }
  props.updateProps({ selected: next })
}

const answerSummary = computed(() => {
  const current = ask.value
  if (!current) return ''
  const labelById = new Map(current.options.map((opt) => [opt.id, opt.label]))
  const picked = current.selected.map((id) => labelById.get(id) ?? id)
  const parts: string[] = []
  if (picked.length > 0) parts.push(list(picked))
  const note = current.note.trim()
  if (note) parts.push(note)
  return parts.join(' · ')
})

const hasAnswer = computed(
  () => (ask.value?.selected.length ?? 0) > 0 || (ask.value?.note.trim().length ?? 0) > 0,
)
</script>

<template>
  <div
    v-if="ask"
    class="doc-ask"
    :class="{
      'is-confirmed': ask.confirmed,
      'is-answered': !ask.confirmed && hasAnswer,
      'is-selected': selected,
    }"
    contenteditable="false"
  >
    <div v-if="ask.confirmed && !expanded" class="doc-ask-collapsed">
      <CheckCircleIcon class="doc-ask-icon is-confirmed" />
      <DouTag size="sm" tone="neutral" class="doc-ask-badge">
        {{ t('artifact.ask.confirmed') }}
      </DouTag>
      <DouText weight="medium" size="sm" truncate class="doc-ask-collapsed-q">
        {{ ask.question }}
      </DouText>
      <DouText variant="secondary" size="sm" truncate class="doc-ask-collapsed-a">
        {{ answerSummary || t('artifact.ask.noAnswer') }}
      </DouText>
      <DouButton size="sm" variant="ghost" class="doc-ask-toggle" @click.stop="expanded = true">
        <template #leading><ChevronRightIcon /></template>
        {{ t('artifact.ask.expand') }}
      </DouButton>
    </div>

    <template v-else>
      <div class="doc-ask-head">
        <CheckCircleIcon v-if="ask.confirmed" class="doc-ask-icon is-confirmed" />
        <HelpCircleIcon v-else class="doc-ask-icon" />
        <DouText weight="medium" class="doc-ask-question">
          {{ ask.question }}
        </DouText>
        <div class="doc-ask-tags">
          <DouTag v-if="ask.confirmed" size="sm" tone="neutral">
            {{ t('artifact.ask.confirmed') }}
          </DouTag>
          <DouTag v-else-if="ask.pick === 'many'" size="sm" tone="neutral">
            {{ t('artifact.ask.multi') }}
          </DouTag>
          <DouTag v-else-if="!hasAnswer" size="sm" tone="primary">
            {{ t('artifact.ask.badge') }}
          </DouTag>
          <DouButton
            v-if="ask.confirmed"
            size="sm"
            variant="ghost"
            class="doc-ask-toggle"
            @click.stop="expanded = false"
          >
            <template #leading><ChevronDownIcon /></template>
            {{ t('artifact.ask.collapse') }}
          </DouButton>
        </div>
      </div>

      <DouText v-if="ask.description" variant="tertiary" size="sm" class="doc-ask-desc">
        {{ ask.description }}
      </DouText>

      <div v-if="ask.options.length > 0 && ask.pick !== 'text'" class="doc-ask-options">
        <button
          v-for="opt in ask.options"
          :key="opt.id"
          type="button"
          class="doc-ask-option"
          :class="{
            'is-picked': selectedSet.has(opt.id),
            'is-recommended': opt.recommended && !selectedSet.has(opt.id),
          }"
          :disabled="!editable || ask.confirmed"
          :aria-pressed="selectedSet.has(opt.id)"
          @click.stop="toggleOption(opt)"
        >
          <span
            class="doc-ask-check"
            :class="{
              'is-multi': ask.pick === 'many',
              'is-picked': selectedSet.has(opt.id),
            }"
          >
            <CheckIcon v-if="selectedSet.has(opt.id)" />
          </span>
          <span class="doc-ask-option-body">
            <span class="doc-ask-option-row">
              <DouText weight="medium" size="sm">{{ opt.label }}</DouText>
              <DouTag v-if="opt.recommended" size="sm" tone="neutral">
                {{ t('artifact.ask.recommended') }}
              </DouTag>
            </span>
            <DouText v-if="opt.note" variant="tertiary" size="sm">
              {{ opt.note }}
            </DouText>
          </span>
        </button>
      </div>

      <div v-if="ask.pick === 'text' || ask.allowNote" class="doc-ask-note">
        <DouInput
          :model-value="noteDraft"
          size="sm"
          :disabled="!editable || ask.confirmed"
          :placeholder="
            ask.pick === 'text'
              ? t('artifact.ask.textPlaceholder')
              : t('artifact.ask.notePlaceholder')
          "
          :aria-label="ask.question"
          @update:model-value="onNoteInput"
          @blur="flushNote"
          @keydown.enter.prevent="flushNote"
        />
      </div>
    </template>
  </div>
</template>

<style scoped>
.doc-ask {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 12px 14px;
  margin: 4px 0;
  border: 1px solid var(--dou-color-border);
  border-radius: var(--dou-radius-md, 10px);
  background: var(--dou-color-surface-subtle, var(--dou-color-surface));
  transition:
    border-color 0.15s ease,
    background-color 0.15s ease;
}

.doc-ask.is-answered {
  border-color: color-mix(in srgb, var(--dou-color-primary) 45%, var(--dou-color-border));
}

.doc-ask.is-confirmed {
  padding: 8px 12px;
  opacity: 0.85;
}

.doc-ask.is-selected {
  outline: 2px solid color-mix(in srgb, var(--dou-color-primary) 45%, transparent);
  outline-offset: 1px;
}

.doc-ask-collapsed {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.doc-ask-collapsed-q {
  flex: 0 1 auto;
  max-width: 45%;
}

.doc-ask-collapsed-a {
  flex: 1 1 0;
  min-width: 0;
}

.doc-ask-head {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  min-width: 0;
}

.doc-ask-icon {
  flex: none;
  width: 16px;
  height: 16px;
  margin-top: 3px;
  color: var(--dou-color-primary);
}

.doc-ask-icon.is-confirmed {
  margin-top: 0;
  color: var(--dou-color-success);
}

.doc-ask-question {
  flex: 1;
  min-width: 0;
  line-height: 1.5;
}

.doc-ask-tags {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  flex: none;
}

.doc-ask-badge {
  flex: none;
}

.doc-ask-toggle {
  flex: none;
  margin-inline-start: auto;
}

.doc-ask-desc {
  padding-inline-start: 24px;
  line-height: 1.5;
}

.doc-ask-options {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding-inline-start: 24px;
}

.doc-ask-option {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  width: 100%;
  padding: 8px 10px;
  border: 1px solid var(--dou-color-border);
  border-radius: var(--dou-radius-sm, 8px);
  background: var(--dou-color-surface, var(--dou-color-bg));
  color: inherit;
  font: inherit;
  text-align: left;
  cursor: pointer;
  transition:
    border-color 0.15s ease,
    background-color 0.15s ease;
}

.doc-ask-option:hover:not(:disabled) {
  border-color: color-mix(in srgb, var(--dou-color-primary) 50%, var(--dou-color-border));
}

.doc-ask-option.is-picked {
  border-color: var(--dou-color-primary);
  background: color-mix(
    in srgb,
    var(--dou-color-primary) 7%,
    var(--dou-color-surface, var(--dou-color-bg))
  );
}

.doc-ask-option:disabled {
  cursor: default;
}

.doc-ask-check {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: none;
  width: 16px;
  height: 16px;
  margin-top: 2px;
  border: 1.5px solid var(--dou-color-border-strong, var(--dou-color-border));
  border-radius: 999px;
  color: #fff;
  font-size: 11px;
  transition:
    border-color 0.15s ease,
    background-color 0.15s ease;
}

.doc-ask-check.is-multi {
  border-radius: 4px;
}

.doc-ask-check.is-picked {
  border-color: var(--dou-color-primary);
  background: var(--dou-color-primary);
}

.doc-ask-option-body {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
  flex: 1;
}

.doc-ask-option-row {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
}

.doc-ask-note {
  padding-inline-start: 24px;
}
</style>
