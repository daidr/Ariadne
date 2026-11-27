<script setup lang="ts">
import { ArrowRightIcon, ChevronLeftIcon, SlidersIcon } from '@dou-ui/icons'
import {
  DouButton,
  DouCheckTag,
  DouCheckTagGroup,
  DouDivider,
  DouDropdown,
  DouDropdownGroup,
  DouDropdownItem,
  DouDropdownRadioGroup,
  DouSegmentedControl,
  DouStatusDot,
  DouText,
  type CheckTagValue,
  type DropdownValue,
} from 'dou-ui'
import { computed, ref } from 'vue'
import { t } from '../i18n'
import {
  PULSE_WINDOW_KEYS,
  type PulseDensity,
  type PulseFilter,
  type PulseGroupBy,
  type PulseLaneCounts,
  type PulseLaneLines,
  type PulseScope,
  type PulseWindowKey,
} from './model'

defineProps<{
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
const laneLines = defineModel<PulseLaneLines>('laneLines', { required: true })
const showConcurrency = defineModel<boolean>('showConcurrency', { required: true })
const showHandoffs = defineModel<boolean>('showHandoffs', { required: true })
const showReturnLine = defineModel<boolean>('showReturnLine', { required: true })
const showLegend = defineModel<boolean>('showLegend', { required: true })

const viewMenuOpen = ref(false)

const scopeOptions = computed(() => [
  { value: 'project' satisfies PulseScope, label: t('pulse.scope.project') },
  { value: 'all' satisfies PulseScope, label: t('pulse.scope.all') },
])

const windowOptions = computed(() =>
  PULSE_WINDOW_KEYS.map((key) => ({
    value: key,
    label: t(`pulse.window.${key}`),
  })),
)

function onFilterChange(value: CheckTagValue | CheckTagValue[] | null) {
  if (value === 'all' || value === 'needs' || value === 'active') {
    filter.value = value
  }
}

function onGroupByChange(value: DropdownValue | undefined) {
  if (value === 'stage' || value === 'node') {
    groupBy.value = value
  }
}

function onDensityChange(value: DropdownValue | undefined) {
  if (value === 'comfortable' || value === 'compact') {
    density.value = value
    if (value === 'compact' && laneLines.value === 'double') {
      laneLines.value = 'single'
    }
  }
}

function onLaneLinesChange(value: DropdownValue | undefined) {
  if (value === 'single' || value === 'double') {
    laneLines.value = value
    if (value === 'double' && density.value === 'compact') {
      density.value = 'comfortable'
    }
  }
}
</script>

<template>
  <header class="pulse-toolbar">
    <div class="pulse-toolbar-left">
      <DouButton variant="ghost" size="sm" @click="emit('back')">
        <template #leading><ChevronLeftIcon aria-hidden="true" /></template>
        {{ t('view.back') }}
      </DouButton>
      <div class="pulse-toolbar-heading">
        <DouText weight="medium" class="pulse-toolbar-title">{{ t('pulse.title') }}</DouText>
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
      <DouCheckTagGroup
        :model-value="filter"
        size="sm"
        round
        class="pulse-filters"
        :aria-label="t('pulse.title')"
        @update:model-value="onFilterChange"
      >
        <DouCheckTag value="all" tone="primary" data-pulse-filter="all">
          <span>{{ t('pulse.filter.all') }}</span>
          <span class="pulse-filter-count">{{ counts.all }}</span>
        </DouCheckTag>
        <DouCheckTag value="needs" tone="warning" data-pulse-filter="needs">
          <template v-if="filter !== 'needs'" #leading>
            <DouStatusDot tone="warning" size="sm" aria-hidden="true" />
          </template>
          <span>{{ t('pulse.filter.needs') }}</span>
          <span class="pulse-filter-count">{{ counts.needs }}</span>
        </DouCheckTag>
        <DouCheckTag value="active" tone="primary" data-pulse-filter="active">
          <template v-if="filter !== 'active'" #leading>
            <DouStatusDot tone="primary" size="sm" aria-hidden="true" />
          </template>
          <span>{{ t('pulse.filter.active') }}</span>
          <span class="pulse-filter-count">{{ counts.active }}</span>
        </DouCheckTag>
      </DouCheckTagGroup>
    </div>

    <div class="pulse-toolbar-right">
      <DouSegmentedControl v-model="windowKey" :options="windowOptions" size="sm" />
      <DouDropdown v-model:open="viewMenuOpen" placement="bottom-end" :hide-on-select="false">
        <DouButton
          size="sm"
          variant="ghost"
          data-pulse-view-menu
          :aria-label="t('pulse.viewMenu.button')"
          :aria-expanded="viewMenuOpen"
        >
          <template #leading><SlidersIcon aria-hidden="true" /></template>
          {{ t('pulse.viewMenu.button') }}
        </DouButton>
        <template #menu>
          <DouDropdownRadioGroup
            v-if="scope === 'project'"
            :model-value="groupBy"
            :label="t('pulse.viewMenu.groupBy')"
            @update:model-value="onGroupByChange"
          >
            <DouDropdownItem value="stage" data-pulse-group-option="stage">
              {{ t('pulse.group.stage') }}
            </DouDropdownItem>
            <DouDropdownItem value="node" data-pulse-group-option="node">
              {{ t('pulse.group.node') }}
            </DouDropdownItem>
          </DouDropdownRadioGroup>

          <DouDropdownRadioGroup
            :model-value="density"
            :label="t('pulse.viewMenu.density')"
            :divided="scope === 'project'"
            @update:model-value="onDensityChange"
          >
            <DouDropdownItem value="comfortable" data-pulse-density-option="comfortable">
              {{ t('pulse.density.comfortable') }}
            </DouDropdownItem>
            <DouDropdownItem value="compact" data-pulse-density-option="compact">
              {{ t('pulse.density.compact') }}
            </DouDropdownItem>
          </DouDropdownRadioGroup>

          <DouDropdownRadioGroup
            :model-value="laneLines"
            :label="t('pulse.viewMenu.laneInfo')"
            divided
            @update:model-value="onLaneLinesChange"
          >
            <DouDropdownItem value="single" data-pulse-lane-lines="single">
              {{ t('pulse.viewMenu.singleLine') }}
            </DouDropdownItem>
            <DouDropdownItem value="double" data-pulse-lane-lines="double">
              {{ t('pulse.viewMenu.doubleLine') }}
            </DouDropdownItem>
          </DouDropdownRadioGroup>

          <DouDropdownGroup :label="t('pulse.viewMenu.display')" divided>
            <DouDropdownItem
              v-model:checked="showConcurrency"
              data-pulse-layer-toggle="concurrency"
            >
              {{ t('pulse.layer.concurrency') }}
            </DouDropdownItem>
            <DouDropdownItem v-model:checked="showHandoffs" data-pulse-layer-toggle="handoffs">
              {{ t('pulse.layer.handoffs') }}
            </DouDropdownItem>
            <DouDropdownItem v-model:checked="showReturnLine" data-pulse-layer-toggle="returnLine">
              {{ t('pulse.layer.returnLine') }}
            </DouDropdownItem>
            <DouDropdownItem v-model:checked="showLegend" data-pulse-layer-toggle="legend">
              {{ t('pulse.layer.legend') }}
            </DouDropdownItem>
          </DouDropdownGroup>
        </template>
      </DouDropdown>
      <DouButton
        size="sm"
        :variant="following ? 'soft' : 'outline'"
        :tone="following ? 'primary' : 'neutral'"
        data-pulse-follow
        @click="emit('snapToNow')"
      >
        <template #leading>
          <DouStatusDot v-if="following" tone="primary" size="sm" ripple aria-hidden="true" />
        </template>
        {{ following ? t('pulse.follow.live') : t('pulse.follow.backToNow') }}
        <template v-if="!following" #trailing>
          <ArrowRightIcon aria-hidden="true" />
        </template>
      </DouButton>
    </div>
  </header>
</template>

<style scoped>
.pulse-toolbar {
  display: flex;
  flex: none;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 8px 12px;
  box-sizing: border-box;
  min-height: 44px;
  padding: 6px 14px;
  background: var(--dou-color-bg-surface);
  border-bottom: 1px solid var(--dou-color-border);
}

.pulse-toolbar-left,
.pulse-toolbar-right {
  display: flex;
  flex-wrap: nowrap;
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

.pulse-toolbar-title {
  white-space: nowrap;
}

.pulse-project-name {
  max-width: 150px;
}

.pulse-toolbar-divider {
  height: 16px;
}

.pulse-filters {
  flex: none;
}

.pulse-filter-count {
  margin-left: 4px;
  font-variant-numeric: tabular-nums;
  opacity: 0.85;
}
</style>
