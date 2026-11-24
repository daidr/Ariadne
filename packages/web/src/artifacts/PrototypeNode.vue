<script setup lang="ts">
import type { ArtifactCommentElement } from '@ariadne/core'
import type { NodeViewProps } from '@dou-ui/flow'
import { CloseIcon, MousePointerClickIcon, SlidersIcon } from '@dou-ui/icons'
import { DouButton, DouPopover, DouTag, DouText } from 'dou-ui'
import { computed, onBeforeUnmount, onMounted, ref, useTemplateRef, watch } from 'vue'
import { t } from '../i18n'
import { registerPrototypeInspector } from './prototype-bridge'
import {
  PROTOTYPE_DEFAULT_VIEWPORT_HEIGHT,
  PROTOTYPE_DEFAULT_VIEWPORT_WIDTH,
  PROTOTYPE_HEADER_HEIGHT,
  buildSandboxedPrototypeDoc,
  countModifiedTweaks,
  effectiveTweakValues,
  extractTweaksFromHtml,
  isValidPrototypeSrc,
  normalizePrototypeTweaks,
  sanitizeCommentElement,
  type PrototypeNodeData,
  type PrototypeTweakSpec,
  type PrototypeTweakValues,
} from './prototype-tweaks'
import PrototypeTweaksPanel from './PrototypeTweaksPanel.vue'

const props = defineProps<NodeViewProps<PrototypeNodeData>>()

const rootEl = useTemplateRef<HTMLElement>('rootEl')
const iframeEl = useTemplateRef<HTMLIFrameElement>('iframeEl')

const frameId = `proto-${props.node.id}-${Math.random().toString(36).slice(2, 8)}`
const interacting = ref(false)
const tweaksOpen = ref(false)

// ---- load raw HTML from inline data.html or project file data.src ----

const fetchedHtml = ref<string | null>(null)
const loading = ref(false)
const loadError = ref(false)

watch(
  () => [props.data.html, props.data.src] as const,
  async ([inlineHtml, src], _, onCleanup) => {
    if (inlineHtml) {
      fetchedHtml.value = null
      loading.value = false
      loadError.value = false
      return
    }
    if (!isValidPrototypeSrc(src)) {
      fetchedHtml.value = null
      loading.value = false
      loadError.value = true
      return
    }
    const controller = new AbortController()
    onCleanup(() => controller.abort())
    loading.value = true
    loadError.value = false
    try {
      const response = await fetch(src, { credentials: 'same-origin', signal: controller.signal })
      if (!response.ok) throw new Error(`HTTP ${response.status}`)
      fetchedHtml.value = await response.text()
    } catch {
      if (!controller.signal.aborted) {
        fetchedHtml.value = null
        loadError.value = true
      }
    } finally {
      if (!controller.signal.aborted) loading.value = false
    }
  },
  { immediate: true },
)

const rawHtml = computed(() => props.data.html ?? fetchedHtml.value ?? '')

// ---- declarative tweaks ----

const tweakSpecs = computed<PrototypeTweakSpec[]>(() => {
  if (Array.isArray(props.data.tweaks) && props.data.tweaks.length > 0) {
    return normalizePrototypeTweaks(props.data.tweaks)
  }
  return extractTweaksFromHtml(rawHtml.value)
})

const liveValues = ref<PrototypeTweakValues>({})

watch(
  [tweakSpecs, () => props.data.tweakValues],
  ([specs, saved]) => {
    const next = effectiveTweakValues(specs, saved)
    liveValues.value = next
    postTweaks(next)
  },
  { immediate: true, deep: true },
)

const modifiedCount = computed(() => countModifiedTweaks(tweakSpecs.value, liveValues.value))

// Rebuild srcdoc only when the source HTML changes; live tweaks update via postMessage without reload.
const sandboxedDoc = ref('')
watch(
  rawHtml,
  (html) => {
    if (!html) {
      sandboxedDoc.value = ''
      return
    }
    const specs = tweakSpecs.value
    const initial = effectiveTweakValues(specs, props.data.tweakValues)
    sandboxedDoc.value = buildSandboxedPrototypeDoc(html, frameId, specs, initial)
  },
  { immediate: true },
)

function postTweaks(values: PrototypeTweakValues) {
  const win = iframeEl.value?.contentWindow
  if (!win || !Object.keys(values).length) return
  win.postMessage({ type: 'ariadne:tweaks', frameId, values: { ...values } }, '*')
}

function onIframeLoad() {
  postTweaks(liveValues.value)
}

function onTweakPreview(next: PrototypeTweakValues) {
  liveValues.value = next
  postTweaks(next)
}

function onTweakCommit(next: PrototypeTweakValues) {
  liveValues.value = next
  postTweaks(next)
  if (props.readonly) return
  const diffOnly: PrototypeTweakValues = {}
  for (const spec of tweakSpecs.value) {
    const val = next[spec.key]
    if (val !== undefined && val !== spec.default) {
      diffOnly[spec.key] = val
    }
  }
  props.updateData({
    tweakValues: Object.keys(diffOnly).length > 0 ? diffOnly : undefined,
  })
}

function onTweakReset() {
  const defaults = effectiveTweakValues(tweakSpecs.value)
  liveValues.value = defaults
  postTweaks(defaults)
  if (!props.readonly) {
    props.updateData({ tweakValues: undefined })
  }
}

// ---- logical viewport scaling ----

const viewportWidth = computed(() =>
  typeof props.data.viewportWidth === 'number' && props.data.viewportWidth > 0
    ? props.data.viewportWidth
    : PROTOTYPE_DEFAULT_VIEWPORT_WIDTH,
)

const viewportHeight = computed(() =>
  typeof props.data.viewportHeight === 'number' && props.data.viewportHeight > 0
    ? props.data.viewportHeight
    : PROTOTYPE_DEFAULT_VIEWPORT_HEIGHT,
)

const stageWidth = computed(() => Math.max((props.node.width ?? 480) - 2, 1))
const stageHeight = computed(() =>
  Math.max((props.node.height ?? 336) - PROTOTYPE_HEADER_HEIGHT - 2, 1),
)

const scale = computed(() =>
  Math.min(stageWidth.value / viewportWidth.value, stageHeight.value / viewportHeight.value),
)

const viewportFrameStyle = computed(() => {
  const s = scale.value
  const rw = viewportWidth.value * s
  const rh = viewportHeight.value * s
  return {
    width: `${viewportWidth.value}px`,
    height: `${viewportHeight.value}px`,
    left: `${(stageWidth.value - rw) / 2}px`,
    top: `${(stageHeight.value - rh) / 2}px`,
    transform: `scale(${s})`,
  }
})

// ---- browse / interact mode ----

function enterInteract() {
  interacting.value = true
}

function exitInteract() {
  interacting.value = false
}

function onWindowKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape' && interacting.value) {
    event.preventDefault()
    event.stopPropagation()
    exitInteract()
  }
}

function onWindowPointerDown(event: PointerEvent) {
  if (!interacting.value) return
  const target = event.target as Node | null
  if (!target || !rootEl.value) return
  if (rootEl.value.contains(target)) return
  if (target instanceof Element && target.closest('.tweaks-panel')) return
  exitInteract()
}

// ---- postMessage bridge (Esc from iframe + semantic comment inspection) ----

const pendingInspects = new Map<string, (element: ArtifactCommentElement | null) => void>()
let inspectSeq = 0

function onWindowMessage(event: MessageEvent) {
  const win = iframeEl.value?.contentWindow
  if (!win || event.source !== win) return
  const msg = event.data as Record<string, unknown> | null
  if (!msg || typeof msg !== 'object' || msg.frameId !== frameId) return
  if (msg.type === 'ariadne:escape') {
    exitInteract()
  } else if (msg.type === 'ariadne:inspect-result' && typeof msg.requestId === 'string') {
    const resolve = pendingInspects.get(msg.requestId)
    if (resolve) {
      pendingInspects.delete(msg.requestId)
      resolve(sanitizeCommentElement(msg.element))
    }
  }
}

function inspectPoint(
  viewportX: number,
  viewportY: number,
): Promise<ArtifactCommentElement | null> {
  const win = iframeEl.value?.contentWindow
  if (!win) return Promise.resolve(null)
  const requestId = `${frameId}-${++inspectSeq}`
  return new Promise((resolve) => {
    pendingInspects.set(requestId, resolve)
    win.postMessage(
      { type: 'ariadne:inspect-point', frameId, requestId, x: viewportX, y: viewportY },
      '*',
    )
  })
}

let unregisterInspector: (() => void) | null = null

onMounted(() => {
  window.addEventListener('message', onWindowMessage)
  window.addEventListener('keydown', onWindowKeydown, true)
  window.addEventListener('pointerdown', onWindowPointerDown, true)
  unregisterInspector = registerPrototypeInspector(props.node.id, inspectPoint)
})

onBeforeUnmount(() => {
  window.removeEventListener('message', onWindowMessage)
  window.removeEventListener('keydown', onWindowKeydown, true)
  window.removeEventListener('pointerdown', onWindowPointerDown, true)
  unregisterInspector?.()
  for (const resolve of pendingInspects.values()) resolve(null)
  pendingInspects.clear()
})

const displayTitle = computed(() => props.data.title || t('artifact.prototype.defaultTitle'))
</script>

<template>
  <div ref="rootEl" class="proto-node" :class="{ 'is-interacting': interacting }">
    <div class="proto-head">
      <div class="proto-title">
        <DouText weight="medium" size="sm" truncate>{{ displayTitle }}</DouText>
        <DouText variant="tertiary" size="sm" class="proto-size">
          {{ viewportWidth }}×{{ viewportHeight }}
        </DouText>
      </div>

      <div class="proto-actions" data-dou-nodrag @pointerdown.stop>
        <DouPopover
          v-if="tweakSpecs.length > 0"
          v-model:open="tweaksOpen"
          placement="right-start"
          width="auto"
          :aria-label="t('artifact.prototype.tweaksTitle')"
        >
          <DouButton
            size="sm"
            :variant="tweaksOpen || modifiedCount > 0 ? 'soft' : 'ghost'"
            :tone="modifiedCount > 0 ? 'primary' : 'neutral'"
            @click.stop
          >
            <template #leading><SlidersIcon /></template>
            {{ t('artifact.prototype.tweaks') }}
            <span v-if="modifiedCount > 0" class="proto-tweak-count">{{ modifiedCount }}</span>
          </DouButton>
          <template #content>
            <PrototypeTweaksPanel
              :specs="tweakSpecs"
              :values="liveValues"
              :readonly="readonly"
              @preview="onTweakPreview"
              @commit="onTweakCommit"
              @reset="onTweakReset"
            />
          </template>
        </DouPopover>

        <template v-if="interacting">
          <DouTag size="sm" tone="primary" class="proto-mode-tag">
            {{ t('artifact.prototype.interacting') }}
          </DouTag>
          <DouButton
            size="sm"
            variant="ghost"
            :aria-label="t('artifact.prototype.exitInteract')"
            :title="t('artifact.prototype.exitInteract')"
            @click.stop="exitInteract"
          >
            <CloseIcon />
          </DouButton>
        </template>
        <DouButton
          v-else
          size="sm"
          variant="ghost"
          :title="t('artifact.prototype.doubleClickHint')"
          @click.stop="enterInteract"
        >
          <template #leading><MousePointerClickIcon /></template>
          {{ t('artifact.prototype.interact') }}
        </DouButton>
      </div>
    </div>

    <div
      class="proto-stage"
      :data-dou-nodrag="interacting ? '' : undefined"
      @pointerdown="interacting ? $event.stopPropagation() : undefined"
      @wheel="interacting ? $event.stopPropagation() : undefined"
      @dblclick.stop="!interacting && enterInteract()"
    >
      <div v-if="loading" class="proto-state">
        <DouText variant="tertiary" size="sm">{{ t('artifact.prototype.loading') }}</DouText>
      </div>
      <div v-else-if="loadError || !sandboxedDoc" class="proto-state">
        <DouText variant="tertiary" size="sm">{{ t('artifact.prototype.loadError') }}</DouText>
      </div>
      <template v-else>
        <iframe
          ref="iframeEl"
          class="proto-iframe"
          :class="{ 'is-interactive': interacting }"
          :style="viewportFrameStyle"
          :srcdoc="sandboxedDoc"
          :title="displayTitle"
          sandbox="allow-scripts"
          referrerpolicy="no-referrer"
          @load="onIframeLoad"
        />
        <div
          v-if="!interacting"
          class="proto-browse-shield"
          :title="t('artifact.prototype.doubleClickHint')"
        />
      </template>
    </div>
  </div>
</template>

<style scoped>
.proto-node {
  display: flex;
  flex-direction: column;
  width: 100%;
  height: 100%;
  overflow: hidden;
  border-radius: inherit;
  background: var(--dou-color-surface, var(--dou-color-bg));
}

.proto-node.is-interacting {
  box-shadow: inset 0 0 0 2px var(--dou-color-primary);
}

.proto-head {
  display: flex;
  flex: none;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  box-sizing: border-box;
  height: 36px;
  padding: 0 6px 0 10px;
  border-bottom: 1px solid var(--dou-color-border-subtle, var(--dou-color-border));
  background: var(--dou-color-surface-subtle, var(--dou-color-surface, var(--dou-color-bg)));
}

.proto-title {
  display: flex;
  flex: 1;
  align-items: center;
  gap: 6px;
  min-width: 0;
}

.proto-size {
  flex: none;
  font-variant-numeric: tabular-nums;
}

.proto-actions {
  display: flex;
  flex: none;
  align-items: center;
  gap: 4px;
}

.proto-tweak-count {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-width: 16px;
  height: 16px;
  margin-left: 4px;
  padding: 0 4px;
  border-radius: 999px;
  background: var(--dou-color-primary);
  color: var(--dou-color-on-primary, #fff);
  font-size: 11px;
  line-height: 1;
}

.proto-mode-tag {
  flex: none;
}

.proto-stage {
  position: relative;
  flex: 1;
  min-height: 0;
  overflow: hidden;
  background: var(--dou-color-bg-subtle, #f8fafc);
}

.proto-iframe {
  position: absolute;
  border: 0;
  background: #fff;
  transform-origin: top left;
  pointer-events: none;
}

.proto-iframe.is-interactive {
  pointer-events: auto;
}

.proto-browse-shield {
  position: absolute;
  inset: 0;
  z-index: 2;
  background: transparent;
}

.proto-state {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 100%;
  height: 100%;
  padding: 16px;
  box-sizing: border-box;
}
</style>
