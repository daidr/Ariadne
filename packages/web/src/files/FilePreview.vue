<script setup lang="ts">
import { CloseIcon, DownloadIcon } from '@dou-ui/icons'
import { DouButton, DouDialog, DouLink, DouSpinner, DouTooltip } from 'dou-ui'
import { computed, defineAsyncComponent, shallowRef, watch } from 'vue'
import { fetchStoredFile } from '../api'
import { t } from '../i18n'
import { fileSrc } from '../markdown/links'
import { parseDelimited, previewFile, previewKind, type SheetPreviewSource } from './preview'

const FileCodePreview = defineAsyncComponent(() => import('./FileCodePreview.vue'))
const FileSheetPreview = defineAsyncComponent(() => import('./FileSheetPreview.vue'))

type Content =
  | { kind: 'text'; text: string }
  | { kind: 'image' | 'video'; url: string }
  | { kind: 'table'; source: SheetPreviewSource }

const maxFileBytes = 10 * 1024 * 1024
const content = shallowRef<Content | null>(null)
const problem = shallowRef<'error' | 'unsupported' | 'tooLarge' | null>(null)
const name = computed(() => previewFile.value?.name || t('filePreview.title'))

watch(previewFile, async (file, _previous, cleanup) => {
  content.value = null
  problem.value = null
  if (!file) return

  const controller = new AbortController()
  const { signal } = controller
  let objectUrl: string | undefined
  cleanup(() => {
    controller.abort()
    if (objectUrl) URL.revokeObjectURL(objectUrl)
  })

  try {
    let mime = file.mime
    let size = file.size
    if (!mime || size === undefined) {
      const response = await fetchStoredFile(file.id, 'HEAD', signal)
      mime = response.headers.get('content-type') ?? ''
      size = Number(response.headers.get('content-length') ?? 0)
    }
    if (signal.aborted) return
    if (size > maxFileBytes) {
      problem.value = 'tooLarge'
      return
    }
    const kind = previewKind(mime, file.name ?? '')
    if (!kind) {
      problem.value = 'unsupported'
      return
    }
    if (kind === 'sheet') {
      content.value = { kind: 'table', source: { fileId: file.id } }
      return
    }

    const response = await fetchStoredFile(file.id, 'GET', signal)
    const blob = await response.blob()
    if (signal.aborted) return
    if (blob.size > maxFileBytes) {
      problem.value = 'tooLarge'
      return
    }
    if (kind === 'image' || kind === 'video') {
      if (previewKind(blob.type, file.name ?? '') !== kind) throw new Error('File type changed')
      objectUrl = URL.createObjectURL(blob)
      content.value = { kind, url: objectUrl }
      return
    }
    const text = await blob.text()
    if (signal.aborted) return
    content.value =
      kind === 'text'
        ? { kind, text }
        : { kind: 'table', source: parseDelimited(text, kind === 'csv' ? ',' : '\t') }
  } catch {
    if (!signal.aborted) problem.value = 'error'
  }
})
</script>

<template>
  <DouDialog
    :open="!!previewFile"
    :aria-label="name"
    :closable="false"
    class="file-preview-dialog"
    fullscreen
    destroy-on-close
    @update:open="(open) => !open && (previewFile = null)"
  >
    <div v-if="previewFile" class="file-preview">
      <header class="file-heading">
        <h2 class="file-name" :title="name">{{ name }}</h2>
        <DouLink
          class="file-download"
          :href="fileSrc(previewFile.id)"
          :download="previewFile.name ?? ''"
          underline="hover"
        >
          <template #leading><DownloadIcon /></template>
          {{ t('filePreview.download') }}
        </DouLink>
        <DouTooltip :content="t('view.close')" placement="bottom">
          <DouButton
            class="file-close"
            size="sm"
            variant="ghost"
            :aria-label="t('view.close')"
            @click="previewFile = null"
          >
            <CloseIcon />
          </DouButton>
        </DouTooltip>
      </header>
      <div v-if="problem" class="file-state" role="status">
        {{ t(`filePreview.${problem}`) }}
      </div>
      <div
        v-else-if="!content"
        class="file-state"
        role="status"
        :aria-label="t('filePreview.loading')"
      >
        <DouSpinner />
      </div>
      <FileCodePreview v-else-if="content.kind === 'text'" :name="name" :text="content.text" />
      <FileSheetPreview
        v-else-if="content.kind === 'table'"
        :key="previewFile.id"
        :name="name"
        :source="content.source"
      />
      <div v-else class="file-media" @error.capture="problem = 'error'">
        <img v-if="content.kind === 'image'" :src="content.url" :alt="name" />
        <video v-else :src="content.url" :aria-label="name" controls playsinline />
      </div>
    </div>
  </DouDialog>
</template>

<style>
.file-preview-dialog {
  --dou-dialog-padding: 0;
}
</style>

<style scoped>
.file-preview {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
}

.file-heading {
  display: flex;
  flex: none;
  align-items: center;
  gap: 8px;
  padding: 4px 8px 4px 16px;
  border-bottom: 1px solid var(--dou-color-border);
}

.file-name {
  min-width: 0;
  margin: 0;
  overflow: hidden;
  font: inherit;
  font-weight: var(--dou-font-weight-medium);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.file-download {
  flex: none;
  font-size: var(--dou-font-size-sm);
  font-weight: normal;
  white-space: nowrap;
}

.file-close {
  flex: none;
  margin-inline-start: auto;
}

.file-state {
  display: grid;
  flex: 1;
  place-items: center;
  color: var(--dou-color-text-muted);
}

.file-media {
  display: grid;
  flex: 1;
  min-height: 0;
  place-items: center;
}

.file-media img,
.file-media video {
  max-width: 100%;
  max-height: 100%;
  min-height: 0;
  object-fit: contain;
}
</style>
