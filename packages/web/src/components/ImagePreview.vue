<script setup lang="ts">
import {
  ChevronLeftIcon,
  ChevronRightIcon,
  CloseIcon,
  DownloadIcon,
  FlipHorizontalIcon,
  FlipVerticalIcon,
  MaximizeIcon,
  RotateCcwIcon,
  RotateCwIcon,
  ZoomInIcon,
  ZoomOutIcon,
} from '@dou-ui/icons'
import {
  downloadBlob,
  useFocusTrap,
  useLayer,
  useScrollLock,
  useTeleport,
  type TeleportTarget,
} from 'dou-ui'
import { computed, ref, useId, useTemplateRef, watch } from 'vue'
import { douLocale } from '../i18n'
import { useImageViewport } from './use-image-viewport'

export type PreviewImageItem = string | { src: string; alt?: string }

let previewLayerCounter = 2500

const props = withDefaults(
  defineProps<{
    images?: readonly PreviewImageItem[]
    loop?: boolean
    minScale?: number
    maxScale?: number
    closeOnMask?: boolean
    teleport?: TeleportTarget
    downloadable?: boolean
  }>(),
  {
    images: () => [],
    loop: false,
    minScale: 0.25,
    maxScale: 5,
    closeOnMask: true,
    teleport: undefined,
    downloadable: false,
  },
)

const open = defineModel<boolean>('open', { default: false })
const index = defineModel<number>('index', { default: 0 })
const emit = defineEmits<{ close: [] }>()

const id = `image-preview-${useId()}`
const teleportTarget = useTeleport(() => props.teleport)
const labels = computed(() => douLocale.value.image)

const list = computed(() =>
  props.images.map((item) => (typeof item === 'string' ? { src: item } : item)),
)
const count = computed(() => list.value.length)
const current = computed(() => {
  if (!count.value) return undefined
  const safeIndex = Math.min(Math.max(0, index.value), count.value - 1)
  return list.value[safeIndex]
})

const failed = ref(false)
const rendered = ref(open.value)
const zIndex = ref(++previewLayerCounter)
const theme = ref<string | undefined>(undefined)

const root = useTemplateRef<HTMLElement>('root')
const stage = useTemplateRef<HTMLElement>('stage')
const image = useTemplateRef<HTMLImageElement>('image')

function close() {
  open.value = false
  emit('close')
}

const viewport = useImageViewport({
  stage,
  image,
  minScale: () => props.minScale,
  maxScale: () => props.maxScale,
  closeOnMask: () => props.closeOnMask,
  onMaskTap: close,
})

watch(
  open,
  (isOpen) => {
    if (!isOpen) return
    rendered.value = true
    zIndex.value = ++previewLayerCounter
    if (typeof document !== 'undefined') {
      theme.value = document.documentElement.dataset.douTheme
    }
  },
  { immediate: true },
)

watch([index, open], () => {
  viewport.reset()
  failed.value = false
})

const layer = useLayer({
  open,
  elements: () => [root.value],
  onEscape: close,
})

useFocusTrap(root, {
  active: open,
  contains: layer.contains,
  initialFocus: () => root.value,
})

useScrollLock(() => open.value)

const canPrev = computed(() => count.value > 1 && (props.loop || index.value > 0))
const canNext = computed(() => count.value > 1 && (props.loop || index.value < count.value - 1))

function go(step: number) {
  if (step < 0 ? !canPrev.value : !canNext.value) return
  index.value = (index.value + step + count.value) % count.value
}

function onKeydown(event: KeyboardEvent) {
  if (event.ctrlKey || event.metaKey || event.altKey) return
  if (event.target instanceof HTMLButtonElement && (event.key === 'Enter' || event.key === ' ')) {
    return
  }
  const actions: Record<string, () => void> = {
    ArrowLeft: () => go(-1),
    ArrowRight: () => go(1),
    '+': () => viewport.zoomBy(1),
    '=': () => viewport.zoomBy(1),
    '-': () => viewport.zoomBy(-1),
    '0': () => viewport.reset(),
    r: () => viewport.turn(90),
    R: () => viewport.turn(-90),
    h: () => viewport.mirror('x'),
    v: () => viewport.mirror('y'),
  }
  const action = actions[event.key]
  if (!action) return
  event.preventDefault()
  action()
}

function saveLink(href: string, name: string) {
  const link = document.createElement('a')
  link.href = href
  link.download = name
  link.target = '_blank'
  link.rel = 'noopener'
  document.body.append(link)
  link.click()
  link.remove()
}

async function downloadCurrent() {
  const src = current.value?.src
  if (!src) return
  const url = new URL(src, location.href)
  const name = decodeURIComponent(url.pathname.split('/').pop() || '') || 'image'
  try {
    const response = await fetch(url)
    if (!response.ok) throw new Error(String(response.status))
    downloadBlob(await response.blob(), name)
  } catch {
    saveLink(url.href, name)
  }
}

const counterLabel = computed(() => {
  if (count.value <= 1) return ''
  const currentIndex = Math.min(Math.max(0, index.value), count.value - 1) + 1
  return labels.value.counter
    .replace('{index}', String(currentIndex))
    .replace('{total}', String(count.value))
})

const scalePercent = computed(() => `${Math.round(viewport.scale.value * 100)}%`)
</script>

<template>
  <Teleport v-if="rendered" :to="teleportTarget.to" :disabled="teleportTarget.disabled">
    <Transition appear name="image-preview" persisted>
      <div
        v-show="open"
        :id="id"
        ref="root"
        role="dialog"
        aria-modal="true"
        tabindex="-1"
        :aria-label="current?.alt || labels.preview"
        class="image-preview"
        :style="{ zIndex }"
        :data-dou-theme="theme"
        @keydown="onKeydown"
      >
        <div ref="stage" class="image-preview__stage" v-bind="viewport.stageListeners">
          <img
            v-if="current && !failed"
            :key="current.src"
            ref="image"
            :src="current.src"
            :alt="current.alt ?? ''"
            :class="[
              'image-preview__image',
              {
                'is-movable': viewport.movable.value,
                'is-interacting': viewport.interacting.value,
              },
            ]"
            :style="{ transform: viewport.transformStyle.value }"
            draggable="false"
            v-bind="viewport.imageListeners"
            @error="failed = true"
          />
          <div v-else-if="current" class="image-preview__error">
            {{ labels.error }}
          </div>
        </div>

        <button
          v-if="count > 1"
          type="button"
          class="image-preview__nav is-prev"
          :disabled="!canPrev"
          :aria-label="labels.prev"
          @click="go(-1)"
        >
          <ChevronLeftIcon />
        </button>

        <button
          v-if="count > 1"
          type="button"
          class="image-preview__nav is-next"
          :disabled="!canNext"
          :aria-label="labels.next"
          @click="go(1)"
        >
          <ChevronRightIcon />
        </button>

        <button
          type="button"
          class="image-preview__close"
          :aria-label="labels.close"
          @click="close"
        >
          <CloseIcon />
        </button>

        <div class="image-preview__toolbar" role="toolbar" :aria-label="labels.preview">
          <span v-if="counterLabel" class="image-preview__counter" aria-live="polite">
            {{ counterLabel }}
          </span>

          <button
            type="button"
            class="image-preview__tool"
            :disabled="viewport.scale.value <= minScale"
            :aria-label="labels.zoomOut"
            @click="viewport.zoomBy(-1)"
          >
            <ZoomOutIcon />
          </button>

          <span class="image-preview__scale" aria-live="polite">{{ scalePercent }}</span>

          <button
            type="button"
            class="image-preview__tool"
            :disabled="viewport.scale.value >= maxScale"
            :aria-label="labels.zoomIn"
            @click="viewport.zoomBy(1)"
          >
            <ZoomInIcon />
          </button>

          <span class="image-preview__divider" aria-hidden="true" />

          <button
            type="button"
            class="image-preview__tool"
            :aria-label="labels.rotateLeft"
            @click="viewport.turn(-90)"
          >
            <RotateCcwIcon />
          </button>

          <button
            type="button"
            class="image-preview__tool"
            :aria-label="labels.rotateRight"
            @click="viewport.turn(90)"
          >
            <RotateCwIcon />
          </button>

          <button
            type="button"
            class="image-preview__tool"
            :aria-label="labels.flipHorizontal"
            :aria-pressed="viewport.flip.value.x < 0"
            @click="viewport.mirror('x')"
          >
            <FlipHorizontalIcon />
          </button>

          <button
            type="button"
            class="image-preview__tool"
            :aria-label="labels.flipVertical"
            :aria-pressed="viewport.flip.value.y < 0"
            @click="viewport.mirror('y')"
          >
            <FlipVerticalIcon />
          </button>

          <button
            type="button"
            class="image-preview__tool"
            :aria-label="labels.reset"
            @click="viewport.reset()"
          >
            <MaximizeIcon />
          </button>

          <template v-if="downloadable && current">
            <span class="image-preview__divider" aria-hidden="true" />
            <button
              type="button"
              class="image-preview__tool"
              :aria-label="labels.download"
              @click="downloadCurrent"
            >
              <DownloadIcon />
            </button>
          </template>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.image-preview {
  --image-preview-chrome: #ffffff1a;
  --image-preview-chrome-hover: #ffffff2e;
  --image-preview-chrome-border: #ffffff1f;
  position: fixed;
  inset: 0;
  color: #ffffffeb;
  font-family: var(--dou-font-family);
  font-size: var(--dou-font-size-sm);
  outline: none;
  background-color: #08090be0;
  -webkit-backdrop-filter: blur(6px);
  backdrop-filter: blur(6px);
  overscroll-behavior: contain;
}

.image-preview__stage {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  overflow: hidden;
  padding: 56px 72px 80px;
  touch-action: none;
  user-select: none;
  -webkit-user-select: none;
}

.image-preview__image {
  max-width: 100%;
  max-height: 100%;
  border-radius: 2px;
  box-shadow: 0 16px 48px #0006;
  transform-origin: center center;
  will-change: transform;
  transition: transform var(--dou-motion-duration-normal) var(--dou-motion-easing);
  user-select: none;
  -webkit-user-select: none;
  -webkit-touch-callout: none;
  touch-action: none;
}

.image-preview__image.is-movable {
  cursor: grab;
}

.image-preview__image.is-interacting {
  transition: none;
}

.image-preview__image.is-movable.is-interacting {
  cursor: grabbing;
}

.image-preview__error {
  color: #fff9;
  font-size: var(--dou-font-size-md);
}

.image-preview__nav,
.image-preview__close,
.image-preview__tool {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  box-sizing: border-box;
  margin: 0;
  padding: 0;
  border: 0;
  color: inherit;
  font: inherit;
  cursor: pointer;
  transition: background-color var(--dou-motion-duration-fast) var(--dou-motion-easing);
}

.image-preview__nav:focus-visible,
.image-preview__close:focus-visible,
.image-preview__tool:focus-visible {
  outline: 2px solid #fffc;
  outline-offset: 2px;
}

.image-preview__nav:disabled,
.image-preview__close:disabled,
.image-preview__tool:disabled {
  opacity: 0.32;
  cursor: not-allowed;
}

.image-preview__nav,
.image-preview__close {
  position: absolute;
  width: 40px;
  height: 40px;
  border: 1px solid var(--image-preview-chrome-border);
  border-radius: var(--dou-radius-full);
  background-color: var(--image-preview-chrome);
}

.image-preview__nav:hover:not(:disabled),
.image-preview__close:hover:not(:disabled) {
  background-color: var(--image-preview-chrome-hover);
}

.image-preview__nav > svg,
.image-preview__close > svg {
  width: 1em;
  height: 1em;
  font-size: 18px;
}

.image-preview__nav {
  top: 50%;
  translate: 0 -50%;
}

.image-preview__nav.is-prev {
  left: 16px;
}

.image-preview__nav.is-next {
  right: 16px;
}

.image-preview__close {
  top: calc(16px + env(safe-area-inset-top, 0px));
  right: calc(16px + env(safe-area-inset-right, 0px));
}

.image-preview__toolbar {
  position: absolute;
  bottom: calc(24px + env(safe-area-inset-bottom, 0px));
  left: 50%;
  translate: -50%;
  display: flex;
  align-items: center;
  gap: 2px;
  max-width: calc(100vw - 24px);
  overflow-x: auto;
  scrollbar-width: none;
  padding: 4px;
  border: 1px solid var(--image-preview-chrome-border);
  border-radius: var(--dou-radius-full);
  background-color: #1e1f22db;
  white-space: nowrap;
  box-shadow: 0 8px 24px #00000052;
}

.image-preview__toolbar::-webkit-scrollbar {
  display: none;
}

.image-preview__tool {
  flex: none;
  width: 32px;
  height: 32px;
  border-radius: var(--dou-radius-full);
  background: transparent;
}

.image-preview__tool:hover:not(:disabled) {
  background-color: var(--image-preview-chrome-hover);
}

.image-preview__tool > svg {
  width: 1em;
  height: 1em;
  font-size: 16px;
}

.image-preview__tool[aria-pressed='true'] {
  background-color: var(--image-preview-chrome-hover);
}

.image-preview__counter,
.image-preview__scale {
  flex: none;
  min-width: 44px;
  padding: 0 6px;
  color: #ffffffb8;
  font-variant-numeric: tabular-nums;
  text-align: center;
}

.image-preview__counter {
  margin-right: 4px;
  padding-right: 10px;
  border-right: 1px solid var(--image-preview-chrome-border);
}

.image-preview__divider {
  flex: none;
  width: 1px;
  height: 16px;
  margin: 0 4px;
  background-color: var(--image-preview-chrome-border);
}

.image-preview-enter-active {
  transition: opacity var(--dou-motion-duration-normal) var(--dou-motion-easing-enter);
}

.image-preview-leave-active {
  transition: opacity var(--dou-motion-duration-fast) var(--dou-motion-easing-exit);
}

.image-preview-enter-from,
.image-preview-leave-to {
  opacity: 0;
}

@media (max-width: 640px) {
  .image-preview__stage {
    padding: calc(56px + env(safe-area-inset-top, 0px)) 12px
      calc(76px + env(safe-area-inset-bottom, 0px));
  }

  .image-preview__nav.is-prev {
    left: 8px;
  }

  .image-preview__nav.is-next {
    right: 8px;
  }

  .image-preview__close {
    top: calc(12px + env(safe-area-inset-top, 0px));
    right: calc(12px + env(safe-area-inset-right, 0px));
  }

  .image-preview__toolbar {
    bottom: calc(16px + env(safe-area-inset-bottom, 0px));
  }
}
</style>
