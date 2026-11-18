<script setup lang="ts">
import { DouSheet, DouSheetTabs, type SheetWorkbook } from '@dou-ui/sheet'
import '@dou-ui/sheet/style.css'
import { DouSpinner, DouText } from 'dou-ui'
import { computed, shallowRef, watch } from 'vue'
import { fetchSheet } from '../api'
import { t } from '../i18n'
import { previewSheetData, type SheetPreviewRows, type SheetPreviewSource } from './preview'

const props = defineProps<{ name: string; source: SheetPreviewSource }>()

const fileId = computed(() => ('fileId' in props.source ? props.source.fileId : null))
const activeSheet = shallowRef(0)
const names = shallowRef<string[]>([])
const loaded = shallowRef<(SheetPreviewRows & { index: number }) | null>(null)
const failed = shallowRef(false)

watch(
  [fileId, activeSheet],
  async ([id, index], _previous, cleanup) => {
    loaded.value = null
    failed.value = false
    if (!id) return
    const controller = new AbortController()
    cleanup(() => controller.abort())
    try {
      const result = await fetchSheet(id, index, controller.signal)
      if (!controller.signal.aborted) {
        names.value = result.sheets
        loaded.value = { index, ...result }
      }
    } catch {
      if (!controller.signal.aborted) failed.value = true
    }
  },
  { immediate: true },
)

const sheet = computed<SheetPreviewRows | null>(() =>
  'rows' in props.source
    ? props.source
    : loaded.value?.index === activeSheet.value
      ? loaded.value
      : null,
)
const book = computed<SheetWorkbook>(() => ({
  version: 1,
  sheets: (fileId.value ? names.value : [props.name]).map((name, index) => {
    const data = index === activeSheet.value ? sheet.value : null
    return { id: String(index), ...previewSheetData(name, data?.rows ?? [], data?.rowNumbers) }
  }),
}))
const status = computed(() =>
  failed.value ? 'error' : !sheet.value ? 'loading' : sheet.value.rows.length ? null : 'empty',
)
</script>

<template>
  <div class="file-sheet-preview">
    <DouSheet
      v-if="book.sheets.length"
      v-model:active-sheet="activeSheet"
      :model-value="book"
      :aria-label="name"
      :col-width="144"
      :bordered="false"
      :file-drop="false"
      :commentable="false"
      :outline="false"
      readonly
      class="file-sheet-grid"
    >
      <template v-if="fileId || sheet?.truncated" #footer>
        <div class="file-sheet-footer">
          <DouSheetTabs v-if="fileId" :addable="false" class="file-sheet-tabs" />
          <DouText v-if="sheet?.truncated" size="sm" class="file-sheet-note">
            {{ t('filePreview.limited') }}
          </DouText>
        </div>
      </template>
    </DouSheet>
    <div
      v-if="status"
      class="file-sheet-state"
      role="status"
      :aria-label="t(`filePreview.${status}`)"
    >
      <DouSpinner v-if="status === 'loading'" />
      <template v-else>{{ t(`filePreview.${status}`) }}</template>
    </div>
  </div>
</template>

<style scoped>
.file-sheet-preview,
.file-sheet-grid {
  flex: 1;
  min-height: 0;
}

.file-sheet-preview {
  position: relative;
  display: flex;
  flex-direction: column;
}

.file-sheet-footer {
  display: flex;
  align-items: center;
  background-color: var(--dou-sheet-header-bg);
  border-top: 1px solid var(--dou-color-border);
}

.file-sheet-tabs {
  flex: 1;
  min-width: 0;
  border-top: 0;
}

.file-sheet-note {
  padding: 4px 12px;
  margin-inline-start: auto;
}

.file-sheet-state {
  position: absolute;
  inset: 0;
  display: grid;
  place-items: center;
  pointer-events: none;
  color: var(--dou-color-text-tertiary);
}
</style>
