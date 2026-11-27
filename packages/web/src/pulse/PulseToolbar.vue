<script setup lang="ts">
import { ArrowRightIcon, BroadcastIcon, ChevronLeftIcon } from '@dou-ui/icons'
import { DouButton, DouDivider, DouSegmentedControl, DouText } from 'dou-ui'
import { computed } from 'vue'
import { t } from '../i18n'
import {
  PULSE_WINDOW_KEYS,
  type PulseFilter,
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
const windowKey = defineModel<PulseWindowKey>('windowKey', { required: true })

const scopeOptions = computed(() => [
  { value: 'project' satisfies PulseScope, label: t('pulse.scope.project') },
  { value: 'all' satisfies PulseScope, label: t('pulse.scope.all') },
])

const filterOptions = computed(() => [
  { value: 'all' satisfies PulseFilter, label: t('pulse.filter.all') },
  { value: 'needs' satisfies PulseFilter, label: t('pulse.filter.needs') },
  { value: 'active' satisfies PulseFilter, label: t('pulse.filter.active') },
])

const windowOptions = computed(() =>
  PULSE_WINDOW_KEYS.map((key) => ({
    value: key,
    label: t(`pulse.window.${key}`),
  })),
)

function filterCount(value: unknown): number {
  if (value === 'needs') return props.counts.needs
  if (value === 'active') return props.counts.active
  return props.counts.all
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
      <DouSegmentedControl
        v-model="filter"
        :options="filterOptions"
        size="sm"
        class="pulse-filters"
        data-pulse-filters
        :aria-label="t('pulse.title')"
      >
        <template #option="{ option }">
          <span class="pulse-filter-option" :data-pulse-filter="option.value">
            <span>{{ option.label }}</span>
            <span
              class="pulse-filter-count"
              :class="{ 'is-needs': option.value === 'needs' && counts.needs > 0 }"
            >
              {{ filterCount(option.value) }}
            </span>
          </span>
        </template>
      </DouSegmentedControl>
    </div>

    <div class="pulse-toolbar-right">
      <DouSegmentedControl
        v-model="windowKey"
        :options="windowOptions"
        size="sm"
        :aria-label="t('pulse.title')"
      />
      <DouButton
        size="sm"
        :variant="following ? 'soft' : 'outline'"
        :tone="following ? 'primary' : 'neutral'"
        data-pulse-follow
        @click="emit('snapToNow')"
      >
        <template v-if="following" #leading>
          <BroadcastIcon aria-hidden="true" />
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

.pulse-filter-option {
  display: inline-flex;
  align-items: center;
  gap: 5px;
}

.pulse-filter-count {
  font-size: 11px;
  font-variant-numeric: tabular-nums;
  color: var(--dou-color-text-tertiary);
}

.pulse-filter-count.is-needs {
  color: var(--dou-color-warning-text);
}
</style>
