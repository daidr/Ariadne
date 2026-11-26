<script setup lang="ts">
import type { ArtifactSummary } from '@ariadne/core'
import { DouMarkdown, DouSkeleton, DouText, useColorMode } from 'dou-ui'
import { computed, ref, useTemplateRef, watch } from 'vue'
import { COLOR_MODE_KEY } from '../color-mode'
import { boardThumbnail, observeThumbnailVisibility } from './board-thumbnail'

/**
 * A small picture of what an artifact holds, drawn from its stored preview: a document's
 * opening as a page, a whiteboard as a prototype cover / lightweight minimap upgraded to
 * a flow SVG, or a dashboard's tiles. Not interactive.
 */
const props = withDefaults(
  defineProps<{
    artifact: ArtifactSummary
    size?: 'default' | 'compact' | 'mini'
  }>(),
  { size: 'default' },
)
const preview = computed(() => props.artifact.preview)

/** Links and images stay out of a picture. */
const noUrl = () => undefined

const coverFailed = ref(false)
const coverImage = computed(() => {
  if (coverFailed.value) return null
  return preview.value.kind === 'whiteboard' && preview.value.previewImage
    ? preview.value.previewImage
    : null
})
const hasPrototype = computed(
  () => preview.value.kind === 'whiteboard' && (preview.value.prototypeCount ?? 0) > 0,
)
const minimapBoxes = computed(() =>
  preview.value.kind === 'whiteboard' && preview.value.minimap?.length ? preview.value.minimap : [],
)

// A whiteboard without a prototype cover image is upgraded lazily to a Flow SVG once near viewport.
const thumb = useTemplateRef<HTMLElement>('thumb')
const picture = ref<string | null>(null)
const { resolved } = useColorMode({ storageKey: COLOR_MODE_KEY })
const boardTarget = computed(() =>
  props.artifact.preview.kind === 'whiteboard' &&
  props.artifact.preview.items &&
  !coverImage.value &&
  props.size !== 'mini'
    ? ([props.artifact.id, props.artifact.updatedAt, resolved.value] as const)
    : null,
)
const noObserver = () => typeof IntersectionObserver === 'undefined'
const visible = ref(noObserver())

watch(
  () => [props.artifact.id, props.artifact.updatedAt] as const,
  ([id], [prevId]) => {
    picture.value = null
    coverFailed.value = false
    if (id !== prevId) {
      visible.value = noObserver()
    }
  },
)

watch(
  [thumb, () => boardTarget.value !== null, () => props.artifact.id],
  ([el, needsBoard], _prev, cleanup) => {
    if (!needsBoard) {
      visible.value = noObserver()
      return
    }
    if (noObserver()) {
      visible.value = true
      return
    }
    visible.value = false
    if (!el) return
    const stop = observeThumbnailVisibility(el, () => {
      visible.value = true
    })
    cleanup(stop)
  },
  { flush: 'post', immediate: true },
)

let asked = 0
watch(
  [boardTarget, visible],
  async ([target, isVisible]) => {
    const ask = ++asked
    if (!target) return void (picture.value = null)
    if (!isVisible) return
    try {
      const url = await boardThumbnail(...target)
      // A newer change may have asked for another picture meanwhile.
      if (ask === asked) picture.value = url
    } catch {
      if (ask === asked) picture.value = null
    }
  },
  { flush: 'post', immediate: true },
)

const rows = computed(() =>
  preview.value.kind === 'dashboard'
    ? Math.max(1, ...preview.value.tiles.map((tile) => tile.y + tile.h))
    : 1,
)
</script>

<template>
  <div
    ref="thumb"
    class="thumb"
    :class="[
      `thumb-${preview.kind}`,
      `thumb-size-${size}`,
      { 'thumb-prototype': hasPrototype, 'thumb-has-cover': Boolean(coverImage) },
    ]"
    inert
    aria-hidden="true"
  >
    <div v-if="size !== 'mini'" class="thumb-sheet thumb-sheet-back" />
    <div class="thumb-sheet" :class="{ 'thumb-sheet-cover': Boolean(coverImage) }">
      <template v-if="preview.kind === 'document'">
        <div v-if="preview.lead.trim()" class="thumb-page">
          <DouMarkdown :content="preview.lead" :sanitize-url="noUrl" />
        </div>
        <DouSkeleton
          v-else
          variant="text"
          :lines="size === 'mini' ? 2 : 4"
          :animated="false"
          class="thumb-blank"
        />
      </template>

      <img
        v-else-if="preview.kind === 'whiteboard' && coverImage"
        :src="coverImage"
        alt=""
        loading="lazy"
        decoding="async"
        class="thumb-cover"
        @error="coverFailed = true"
      />

      <img
        v-else-if="preview.kind === 'whiteboard' && picture"
        :src="picture"
        alt=""
        class="thumb-board"
      />

      <div v-else-if="preview.kind === 'whiteboard' && minimapBoxes.length" class="thumb-minimap">
        <span
          v-for="(box, index) in minimapBoxes"
          :key="index"
          class="minimap-box"
          :class="`minimap-${box.kind}`"
          :style="{
            left: `${box.x * 100}%`,
            top: `${box.y * 100}%`,
            width: `${Math.max(box.w * 100, 6)}%`,
            height: `${Math.max(box.h * 100, 8)}%`,
          }"
        />
      </div>

      <div
        v-else-if="preview.kind === 'dashboard' && preview.tiles.length"
        class="thumb-grid"
        :style="{ gridTemplateRows: `repeat(${rows}, ${size === 'mini' ? '6px' : '14px'})` }"
      >
        <div
          v-for="(tile, index) in preview.tiles"
          :key="index"
          class="grid-tile"
          :class="`tile-${tile.type}`"
          :style="{
            gridColumn: `${tile.x + 1} / span ${tile.w}`,
            gridRow: `${tile.y + 1} / span ${tile.h}`,
          }"
        >
          <DouText
            v-if="size !== 'mini' && tile.title"
            variant="tertiary"
            truncate
            class="tile-title"
          >
            {{ tile.title }}
          </DouText>
          <span class="tile-mark" />
        </div>
      </div>

      <DouSkeleton
        v-else
        variant="text"
        :lines="size === 'mini' ? 2 : 4"
        :animated="false"
        class="thumb-blank"
      />
    </div>
  </div>
</template>

<style scoped>
.thumb {
  --thumb-tint: var(--dou-color-primary-soft);
  --thumb-ink: var(--dou-color-primary);
  position: relative;
  height: 148px;
  overflow: hidden;
  background: var(--thumb-tint);
}

.thumb-size-compact {
  height: 116px;
}

.thumb-size-mini {
  width: 68px;
  height: 46px;
  border-radius: var(--dou-radius-control);
  flex: none;
}

.thumb-whiteboard {
  --thumb-tint: var(--dou-color-warning-soft);
  --thumb-ink: var(--dou-color-warning);
}

.thumb-prototype {
  --thumb-tint: color-mix(in srgb, var(--dou-color-chart-4) 14%, var(--dou-color-bg-muted));
  --thumb-ink: var(--dou-color-chart-4);
}

.thumb-dashboard {
  --thumb-tint: var(--dou-color-success-soft);
  --thumb-ink: var(--dou-color-success);
}

/* A sheet of paper standing in the tint, its bottom cut off by the card. */
.thumb-sheet {
  position: absolute;
  inset: 24px 12% -12px;
  overflow: hidden;
  padding: 14px 16px;
  border: 1px solid var(--dou-color-border);
  border-radius: 10px;
  background: var(--dou-color-bg-surface);
}

.thumb-size-compact .thumb-sheet {
  inset: 16px 10% -10px;
  padding: 10px 12px;
}

.thumb-size-mini .thumb-sheet {
  inset: 5px 6px -4px;
  padding: 4px 5px;
  border-radius: 5px;
}

.thumb-sheet-cover {
  inset: 14px 8% -4px;
  padding: 0;
}

.thumb-size-compact .thumb-sheet-cover {
  inset: 10px 7% -4px;
}

.thumb-size-mini .thumb-sheet-cover {
  inset: 0;
  border: 0;
  border-radius: inherit;
}

.thumb-sheet-back {
  inset: 18px 10% -12px 14%;
  transform: rotate(2deg);
  opacity: 0.6;
}

.thumb-size-compact .thumb-sheet-back {
  inset: 12px 8% -10px 12%;
}

/* Laid out at twice the size and shrunk, so the small text stays crisp. */
.thumb-page {
  width: 200%;
  transform: scale(0.5);
  transform-origin: top left;
  font-size: 13px;
  pointer-events: none;
}

.thumb-size-mini .thumb-page {
  width: 400%;
  transform: scale(0.25);
}

.thumb-blank {
  width: 70%;
}

.thumb-cover {
  display: block;
  width: 100%;
  height: 100%;
  object-fit: cover;
  object-position: top center;
}

.thumb-board {
  display: block;
  width: 100%;
  height: 100%;
  object-fit: contain;
  object-position: top center;
}

.thumb-minimap {
  position: relative;
  width: 100%;
  height: 100%;
  min-height: 76px;
}

.thumb-size-mini .thumb-minimap {
  min-height: 32px;
}

.minimap-box {
  position: absolute;
  box-sizing: border-box;
  border: 1px solid var(--dou-color-border-strong);
  border-radius: 3px;
  background: var(--dou-color-bg-muted);
}

.minimap-group {
  border-style: dashed;
  background: transparent;
}

.minimap-option {
  border-color: var(--dou-color-primary);
  background: var(--dou-color-primary-soft);
}

.minimap-prototype {
  border-color: var(--dou-color-chart-4);
  background: color-mix(in srgb, var(--dou-color-chart-4) 18%, var(--dou-color-bg-surface));
}

.minimap-sticky {
  border-color: var(--dou-color-warning);
  background: var(--dou-color-warning-soft);
}

.minimap-text {
  border-color: transparent;
  background: var(--dou-color-border);
  opacity: 0.65;
}

.thumb-grid {
  display: grid;
  grid-template-columns: repeat(12, minmax(0, 1fr));
  gap: 4px;
}

.thumb-size-mini .thumb-grid {
  gap: 2px;
}

.grid-tile {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
  padding: 4px 6px;
  overflow: hidden;
  border: 1px solid var(--dou-color-border-subtle);
  border-radius: 4px;
  background: var(--dou-color-bg-muted);
}

.thumb-size-mini .grid-tile {
  padding: 2px;
  border-radius: 2px;
}

.tile-title {
  font-size: 8px;
  line-height: 1.2;
}

/* A hint of what the widget shows: a figure, bars, rows. */
.tile-mark {
  flex: none;
  width: 40%;
  height: 4px;
  border-radius: 2px;
  background: var(--thumb-ink);
  opacity: 0.5;
}

.tile-table .tile-mark,
.tile-pivot .tile-mark {
  width: 100%;
  height: 2px;
  background: var(--dou-color-border-strong);
  box-shadow:
    0 5px 0 var(--dou-color-border-strong),
    0 10px 0 var(--dou-color-border-strong);
}

.tile-text .tile-mark {
  width: 80%;
  height: 2px;
  background: var(--dou-color-border-strong);
}
</style>
