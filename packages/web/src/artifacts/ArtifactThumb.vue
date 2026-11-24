<script setup lang="ts">
import type { ArtifactSummary } from '@ariadne/core'
import { DouMarkdown, DouSkeleton, DouText, useColorMode } from 'dou-ui'
import { computed, ref, useTemplateRef, watch } from 'vue'
import { COLOR_MODE_KEY } from '../color-mode'
import { boardThumbnail, observeThumbnailVisibility } from './board-thumbnail'

/**
 * A small picture of what an artifact holds, drawn from its stored preview: a document's
 * opening as a page, a whiteboard as the board draws it, a dashboard's tiles. Not interactive.
 */
const props = defineProps<{ artifact: ArtifactSummary }>()
const preview = computed(() => props.artifact.preview)

/** Links and images stay out of a picture. */
const noUrl = () => undefined

// A whiteboard is drawn from its content, deferred until its card approaches the viewport.
const thumb = useTemplateRef<HTMLElement>('thumb')
const picture = ref<string | null>(null)
const { resolved } = useColorMode({ storageKey: COLOR_MODE_KEY })
const boardTarget = computed(() =>
  props.artifact.preview.kind === 'whiteboard' && props.artifact.preview.items
    ? ([props.artifact.id, props.artifact.updatedAt, resolved.value] as const)
    : null,
)
const noObserver = () => typeof IntersectionObserver === 'undefined'
const visible = ref(noObserver())

watch(
  () => props.artifact.id,
  () => {
    picture.value = null
    visible.value = noObserver()
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
  <div ref="thumb" class="thumb" :class="`thumb-${preview.kind}`" inert aria-hidden="true">
    <div class="thumb-sheet thumb-sheet-back" />
    <div class="thumb-sheet">
      <template v-if="preview.kind === 'document'">
        <div v-if="preview.lead.trim()" class="thumb-page">
          <DouMarkdown :content="preview.lead" :sanitize-url="noUrl" />
        </div>
        <DouSkeleton v-else variant="text" :lines="4" :animated="false" class="thumb-blank" />
      </template>

      <img
        v-else-if="preview.kind === 'whiteboard' && picture"
        :src="picture"
        alt=""
        class="thumb-board"
      />

      <div
        v-else-if="preview.kind === 'dashboard' && preview.tiles.length"
        class="thumb-grid"
        :style="{ gridTemplateRows: `repeat(${rows}, 14px)` }"
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
          <DouText v-if="tile.title" variant="tertiary" truncate class="tile-title">
            {{ tile.title }}
          </DouText>
          <span class="tile-mark" />
        </div>
      </div>

      <DouSkeleton v-else variant="text" :lines="4" :animated="false" class="thumb-blank" />
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

.thumb-whiteboard {
  --thumb-tint: var(--dou-color-warning-soft);
  --thumb-ink: var(--dou-color-warning);
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

.thumb-sheet-back {
  inset: 18px 10% -12px 14%;
  transform: rotate(2deg);
  opacity: 0.6;
}

/* Laid out at twice the size and shrunk, so the small text stays crisp. */
.thumb-page {
  width: 200%;
  transform: scale(0.5);
  transform-origin: top left;
  font-size: 13px;
  pointer-events: none;
}

.thumb-blank {
  width: 70%;
}

.thumb-board {
  display: block;
  width: 100%;
  height: 100%;
  object-fit: contain;
  object-position: top center;
}

.thumb-grid {
  display: grid;
  grid-template-columns: repeat(12, minmax(0, 1fr));
  gap: 4px;
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
