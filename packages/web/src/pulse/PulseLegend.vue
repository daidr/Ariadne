<script setup lang="ts">
import { t } from '../i18n'
</script>

<template>
  <footer class="pulse-legend" data-pulse-legend>
    <div class="pulse-legend-items">
      <span class="pulse-legend-item">
        <span class="pulse-swatch is-active" aria-hidden="true" />
        {{ t('pulse.legend.active') }}
      </span>
      <span class="pulse-legend-item">
        <span class="pulse-swatch is-attention" aria-hidden="true" />
        {{ t('pulse.legend.attention') }}
      </span>
      <span class="pulse-legend-item">
        <span class="pulse-swatch is-danger" aria-hidden="true" />
        {{ t('pulse.legend.danger') }}
      </span>
      <span class="pulse-legend-item">
        <span class="pulse-swatch is-passive" aria-hidden="true" />
        {{ t('pulse.legend.passive') }}
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
  min-height: 32px;
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
  gap: 6px;
  white-space: nowrap;
}

.pulse-swatch {
  display: inline-block;
  width: 14px;
  height: 9px;
  border-radius: 3px;
  border: 1px solid transparent;
}

.pulse-swatch.is-active {
  background: var(--dou-color-primary-soft);
  border-color: color-mix(in srgb, var(--dou-color-primary) 42%, transparent);
}

.pulse-swatch.is-attention {
  background: var(--dou-color-warning-soft);
  border-color: color-mix(in srgb, var(--dou-color-warning) 48%, transparent);
}

.pulse-swatch.is-danger {
  background: var(--dou-color-danger-soft);
  border-color: color-mix(in srgb, var(--dou-color-danger) 48%, transparent);
}

.pulse-swatch.is-passive {
  background: var(--dou-color-bg-muted);
  border: 1px dashed var(--dou-color-border-strong);
}

.pulse-swatch-diamond {
  display: inline-block;
  width: 7px;
  height: 7px;
  border-radius: 1.5px;
  background: var(--dou-color-success);
  transform: rotate(45deg);
}

.pulse-swatch-return {
  display: inline-block;
  width: 0;
  height: 11px;
  border-left: 1.5px dashed color-mix(in srgb, var(--dou-color-primary) 60%, transparent);
}
</style>
