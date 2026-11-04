<script setup lang="ts">
import type { ArtifactPreview } from '@ariadne/core'
import { DouMarkdown, DouSkeleton, DouText } from 'dou-ui'
import { computed } from 'vue'

/**
 * A small picture of what an artifact holds, drawn from its stored preview: a document's
 * opening as a page, a whiteboard's boxes and lines, a dashboard's tiles. Not interactive.
 */
const props = defineProps<{ preview: ArtifactPreview }>()

/** Links and images stay out of a picture. */
const noUrl = () => undefined

const PAD = 24
const board = computed(() => {
  if (props.preview.kind !== 'whiteboard' || !props.preview.boxes.length) return null
  const { boxes, lines } = props.preview
  const left = Math.min(...boxes.map((b) => b.x)) - PAD
  const top = Math.min(...boxes.map((b) => b.y)) - PAD
  const right = Math.max(...boxes.map((b) => b.x + b.w)) + PAD
  const bottom = Math.max(...boxes.map((b) => b.y + b.h)) + PAD
  return { viewBox: `${left} ${top} ${right - left} ${bottom - top}`, boxes, lines }
})

const rows = computed(() =>
  props.preview.kind === 'dashboard'
    ? Math.max(1, ...props.preview.tiles.map((tile) => tile.y + tile.h))
    : 1,
)
</script>

<template>
  <div class="thumb" :class="`thumb-${preview.kind}`" inert aria-hidden="true">
    <div class="thumb-sheet thumb-sheet-back" />
    <div class="thumb-sheet">
      <template v-if="preview.kind === 'document'">
        <div v-if="preview.lead.trim()" class="thumb-page">
          <DouMarkdown :content="preview.lead" :sanitize-url="noUrl" />
        </div>
        <DouSkeleton v-else variant="text" :lines="4" :animated="false" class="thumb-blank" />
      </template>

      <svg
        v-else-if="preview.kind === 'whiteboard' && board"
        class="thumb-board"
        :viewBox="board.viewBox"
        preserveAspectRatio="xMidYMin meet"
      >
        <line
          v-for="(line, index) in board.lines"
          :key="`l${index}`"
          :x1="line[0]"
          :y1="line[1]"
          :x2="line[2]"
          :y2="line[3]"
          class="board-line"
        />
        <g v-for="(box, index) in board.boxes" :key="index" :class="`board-${box.type}`">
          <ellipse
            v-if="box.shape === 'ellipse'"
            :cx="box.x + box.w / 2"
            :cy="box.y + box.h / 2"
            :rx="box.w / 2"
            :ry="box.h / 2"
            class="board-box"
          />
          <rect
            v-else-if="box.type !== 'text' && box.type !== 'ink'"
            :x="box.x"
            :y="box.y"
            :width="box.w"
            :height="box.h"
            rx="8"
            class="board-box"
          />
          <text
            v-if="box.text"
            :x="box.x + box.w / 2"
            :y="box.y + box.h / 2"
            text-anchor="middle"
            dominant-baseline="central"
            class="board-text"
          >
            {{ box.text }}
          </text>
        </g>
      </svg>

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
}

.board-line {
  stroke: var(--dou-color-border-strong);
  stroke-width: 2;
  vector-effect: non-scaling-stroke;
}

.board-box {
  fill: var(--dou-color-bg-muted);
  stroke: var(--dou-color-border-strong);
  stroke-width: 1;
  vector-effect: non-scaling-stroke;
}

.board-sticky .board-box {
  fill: var(--dou-color-warning-soft);
  stroke: none;
}

.board-image .board-box {
  fill: var(--dou-color-neutral-soft);
  stroke: none;
}

.board-text {
  fill: var(--dou-color-text-secondary);
  font-size: 18px;
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
