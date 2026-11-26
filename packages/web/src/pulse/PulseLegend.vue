<script setup lang="ts">
import { t } from '../i18n'
</script>

<template>
  <footer class="pulse-legend">
    <div class="pulse-legend-items">
      <span class="pulse-legend-item">
        <span class="pulse-swatch is-working" aria-hidden="true" />
        {{ t('pulse.stage.working') }}
      </span>
      <span class="pulse-legend-item">
        <span class="pulse-swatch is-reviewing" aria-hidden="true" />
        {{ t('pulse.stage.reviewing') }}
      </span>
      <span class="pulse-legend-item">
        <span class="pulse-swatch is-ci" aria-hidden="true" />
        {{ t('pulse.stage.waiting_ci') }}
      </span>
      <span class="pulse-legend-item">
        <span class="pulse-swatch is-waiting-user" aria-hidden="true" />
        {{ t('pulse.stage.waiting_user') }}
      </span>
      <span class="pulse-legend-item">
        <span class="pulse-swatch is-blocked" aria-hidden="true" />
        {{ t('pulse.stage.blocked') }}
      </span>
      <span class="pulse-legend-item">
        <span class="pulse-swatch is-queued" aria-hidden="true" />
        {{ t('pulse.stage.queued') }}
      </span>
      <span class="pulse-legend-item">
        <span class="pulse-swatch-diamond" aria-hidden="true" />
        {{ t('pulse.legend.milestone') }}
      </span>
      <span class="pulse-legend-item">
        <span class="pulse-swatch-return" aria-hidden="true" />
        {{ t('pulse.legend.returnLine') }}
      </span>
    </div>
    <span class="pulse-legend-hint">{{ t('pulse.legend.hint') }}</span>
  </footer>
</template>

<style scoped>
.pulse-legend {
  display: flex;
  flex: none;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 8px 16px;
  min-height: 34px;
  padding: 4px 14px;
  background: var(--dou-color-bg-surface);
  border-top: 1px solid var(--dou-color-border);
  font-size: 11.5px;
  color: var(--dou-color-text-secondary);
}

.pulse-legend-items {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 14px;
}

.pulse-legend-item {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  white-space: nowrap;
}

.pulse-swatch {
  display: inline-block;
  width: 12px;
  height: 8px;
  border-radius: 2px;
}

.pulse-swatch.is-working {
  background: var(--dou-color-primary);
}

.pulse-swatch.is-reviewing {
  background: var(--dou-color-chart-6);
}

.pulse-swatch.is-ci {
  background: color-mix(in srgb, var(--dou-color-chart-4) 14%, var(--dou-color-bg-surface));
  border: 1px dashed var(--dou-color-chart-4);
}

.pulse-swatch.is-waiting-user {
  background: var(--dou-color-warning-soft);
  border: 1px solid var(--dou-color-warning);
}

.pulse-swatch.is-blocked {
  background: var(--dou-color-danger);
}

.pulse-swatch.is-queued {
  background: var(--dou-color-bg-muted);
  border: 1px dashed var(--dou-color-border-strong);
}

.pulse-swatch-diamond {
  display: inline-block;
  width: 7px;
  height: 7px;
  border-radius: 1px;
  background: var(--dou-color-success);
  transform: rotate(45deg);
}

.pulse-swatch-return {
  display: inline-block;
  width: 0;
  height: 11px;
  border-left: 1.5px dashed color-mix(in srgb, var(--dou-color-primary) 60%, transparent);
}

.pulse-legend-hint {
  color: var(--dou-color-text-tertiary);
  font-variant-numeric: tabular-nums;
}
</style>
