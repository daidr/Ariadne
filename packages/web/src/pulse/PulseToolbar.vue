<script setup lang="ts">
import { ArrowRightIcon, ChevronLeftIcon, LayersIcon, LayoutRowsIcon } from '@dou-ui/icons'
import {
  DouButton,
  DouDivider,
  DouPopover,
  DouSegmentedControl,
  DouStatusDot,
  DouSwitch,
  DouText,
} from 'dou-ui'
import { computed, ref } from 'vue'
import { t } from '../i18n'
import {
  PULSE_WINDOW_KEYS,
  type PulseDensity,
  type PulseFilter,
  type PulseGroupBy,
  type PulseLaneCounts,
  type PulseScope,
  type PulseWindowKey,
} from './model'

const props = defineProps<{
  projectName: string | null
  counts: PulseLaneCounts
  following: boolean
}>()

const emit = defineEmits<{
  back: []
  snapToNow: []
}>()

const scope = defineModel<PulseScope>('scope', { default: 'project' })
const filter = defineModel<PulseFilter>('filter', { required: true })
const groupBy = defineModel<PulseGroupBy>('groupBy', { required: true })
const windowKey = defineModel<PulseWindowKey>('windowKey', { required: true })
const density = defineModel<PulseDensity>('density', { required: true })
const showConcurrency = defineModel<boolean>('showConcurrency', { required: true })
const showHandoffs = defineModel<boolean>('showHandoffs', { required: true })
const showReturnLine = defineModel<boolean>('showReturnLine', { required: true })

const layersOpen = ref(false)

const scopeOptions = computed(() => [
  { value: 'project' satisfies PulseScope, label: t('pulse.scope.project') },
  { value: 'all' satisfies PulseScope, label: t('pulse.scope.all') },
])

const groupOptions = computed(() => [
  { value: 'stage' satisfies PulseGroupBy, label: t('pulse.group.stage') },
  { value: 'node' satisfies PulseGroupBy, label: t('pulse.group.node') },
])

const windowOptions = computed(() =>
  PULSE_WINDOW_KEYS.map((key) => ({
    value: key,
    label: t(`pulse.window.${key}`),
  })),
)

const densityLabel = computed(() =>
  t(density.value === 'compact' ? 'pulse.density.compact' : 'pulse.density.comfortable'),
)

const densityTooltip = computed(() => t('pulse.density.toggle', { mode: densityLabel.value }))

function toggleDensity() {
  density.value = density.value === 'compact' ? 'comfortable' : 'compact'
}
</script>

<template>
  <div class="pulse-toolbar">
    <div class="pulse-toolbar-left">
      <DouButton variant="ghost" size="sm" @click="emit('back')">
        <template #leading><ChevronLeftIcon aria-hidden="true" /></template>
        {{ t('view.back') }}
      </DouButton>
      <div class="pulse-toolbar-heading">
        <DouText weight="medium">{{ t('pulse.title') }}</DouText>
        <DouText
          v-if="scope === 'project' && projectName"
          size="sm"
          variant="tertiary"
          truncate
          class="pulse-project-name"
        >
          {{ projectName }}
        </DouText>
      </div>
      <DouSegmentedControl
        v-model="scope"
        :options="scopeOptions"
        size="sm"
        data-pulse-scope
        :aria-label="t('pulse.scope.aria')"
      />
      <DouDivider direction="vertical" class="pulse-toolbar-divider" />
      <div class="pulse-filters" role="group" :aria-label="t('pulse.title')">
        <button
          type="button"
          class="pulse-filter-pill"
          :class="{ 'is-active': filter === 'all' }"
          data-pulse-filter="all"
          :aria-pressed="filter === 'all'"
          @click="filter = 'all'"
        >
          <span>{{ t('pulse.filter.all') }}</span>
          <span class="pulse-mono">{{ counts.all }}</span>
        </button>
        <button
          type="button"
          class="pulse-filter-pill"
          :class="{ 'is-active-warning': filter === 'needs' }"
          data-pulse-filter="needs"
          :aria-pressed="filter === 'needs'"
          @click="filter = 'needs'"
        >
          <span class="pulse-dot pulse-dot-warning" aria-hidden="true" />
          <span>{{ t('pulse.filter.needs') }}</span>
          <span class="pulse-mono">{{ counts.needs }}</span>
        </button>
        <button
          type="button"
          class="pulse-filter-pill"
          :class="{ 'is-active': filter === 'active' }"
          data-pulse-filter="active"
          :aria-pressed="filter === 'active'"
          @click="filter = 'active'"
        >
          <span class="pulse-dot pulse-dot-primary" aria-hidden="true" />
          <span>{{ t('pulse.filter.active') }}</span>
          <span class="pulse-mono">{{ counts.active }}</span>
        </button>
      </div>
    </div>

    <div class="pulse-toolbar-right">
      <DouSegmentedControl
        v-if="scope === 'project'"
        v-model="groupBy"
        :options="groupOptions"
        size="sm"
      />
      <DouSegmentedControl v-model="windowKey" :options="windowOptions" size="sm" />
      <DouButton
        size="sm"
        :variant="density === 'compact' ? 'soft' : 'ghost'"
        data-pulse-density
        :aria-pressed="density === 'compact'"
        :aria-label="densityTooltip"
        :title="densityTooltip"
        @click="toggleDensity"
      >
        <template #leading><LayoutRowsIcon aria-hidden="true" /></template>
        {{ densityLabel }}
      </DouButton>
      <DouPopover
        v-model:open="layersOpen"
        trigger="click"
        placement="bottom-end"
        :width="236"
        :title="t('pulse.layers')"
      >
        <DouButton
          size="sm"
          variant="ghost"
          data-pulse-layers
          :aria-label="t('pulse.layers')"
          :title="t('pulse.layers')"
          :aria-expanded="layersOpen"
        >
          <LayersIcon aria-hidden="true" />
        </DouButton>
        <template #content>
          <div class="pulse-layers-list">
            <label class="pulse-layer-row">
              <DouText size="sm">{{ t('pulse.layer.concurrency') }}</DouText>
              <DouSwitch v-model="showConcurrency" size="sm" />
            </label>
            <label class="pulse-layer-row">
              <DouText size="sm">{{ t('pulse.layer.handoffs') }}</DouText>
              <DouSwitch v-model="showHandoffs" size="sm" />
            </label>
            <label class="pulse-layer-row">
              <DouText size="sm">{{ t('pulse.layer.returnLine') }}</DouText>
              <DouSwitch v-model="showReturnLine" size="sm" />
            </label>
          </div>
        </template>
      </DouPopover>
      <DouButton
        size="sm"
        :variant="following ? 'soft' : 'outline'"
        :tone="following ? 'primary' : 'neutral'"
        data-pulse-follow
        @click="emit('snapToNow')"
      >
        <template #leading>
          <DouStatusDot v-if="following" tone="primary" ripple aria-hidden="true" />
        </template>
        {{ following ? t('pulse.follow.live') : t('pulse.follow.backToNow') }}
        <template v-if="!following" #trailing>
          <ArrowRightIcon aria-hidden="true" />
        </template>
      </DouButton>
    </div>
  </div>
</template>

<style scoped>
.pulse-toolbar {
  display: flex;
  flex: none;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 8px 12px;
  min-height: 44px;
  padding: 6px 14px;
  background: var(--dou-color-bg-surface);
  border-bottom: 1px solid var(--dou-color-border);
}

.pulse-toolbar-left,
.pulse-toolbar-right {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.pulse-toolbar-heading {
  display: flex;
  align-items: baseline;
  gap: 8px;
  min-width: 0;
}

.pulse-project-name {
  max-width: 180px;
}

.pulse-toolbar-divider {
  height: 16px;
}

.pulse-filters {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}

.pulse-filter-pill {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  height: 26px;
  padding: 0 9px;
  border-radius: 9999px;
  border: 1px solid var(--dou-color-border);
  background: var(--dou-color-bg-surface);
  color: var(--dou-color-text-secondary);
  font: inherit;
  font-size: 12px;
  font-weight: 500;
  cursor: pointer;
  transition:
    border-color 0.15s ease,
    background-color 0.15s ease,
    color 0.15s ease;
}

.pulse-filter-pill:hover {
  border-color: var(--dou-color-border-strong);
  color: var(--dou-color-text);
}

.pulse-filter-pill:focus-visible {
  outline: 2px solid var(--dou-color-focus);
  outline-offset: 1px;
}

.pulse-filter-pill.is-active {
  border-color: var(--dou-color-primary);
  background: var(--dou-color-primary-soft);
  color: var(--dou-color-primary-text);
}

.pulse-filter-pill.is-active-warning {
  border-color: var(--dou-color-warning);
  background: var(--dou-color-warning-soft);
  color: var(--dou-color-warning-text);
}

.pulse-dot {
  width: 7px;
  height: 7px;
  border-radius: 9999px;
  flex: none;
  display: inline-block;
}

.pulse-dot-primary {
  background: var(--dou-color-primary);
}

.pulse-dot-warning {
  background: var(--dou-color-warning);
}

.pulse-mono {
  font-variant-numeric: tabular-nums;
}

.pulse-layers-list {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.pulse-layer-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  cursor: pointer;
}
</style>
