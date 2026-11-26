<script setup lang="ts">
import { RotateCcwIcon } from '@dou-ui/icons'
import {
  DouButton,
  DouCheckTagGroup,
  DouColorPicker,
  DouInput,
  DouSegmentedControl,
  DouSelect,
  DouSlider,
  DouSwitch,
  DouText,
} from 'dou-ui'
import { computed } from 'vue'
import { t } from '../i18n'
import {
  countModifiedTweaks,
  groupTweaksBySection,
  resolveEnumTweakControl,
  type EnumTweakControl,
  type PrototypeTweakSpec,
  type PrototypeTweakValue,
  type PrototypeTweakValues,
} from './prototype-tweaks'

const props = defineProps<{
  specs: PrototypeTweakSpec[]
  values: PrototypeTweakValues
  readonly?: boolean
}>()

const emit = defineEmits<{
  preview: [values: PrototypeTweakValues]
  commit: [values: PrototypeTweakValues]
  reset: []
}>()

const groups = computed(() => groupTweaksBySection(props.specs))
const modifiedCount = computed(() => countModifiedTweaks(props.specs, props.values))

function withValue(key: string, value: PrototypeTweakValue): PrototypeTweakValues {
  return { ...props.values, [key]: value }
}

function onSliderPreview(spec: PrototypeTweakSpec, raw: number | [number, number] | undefined) {
  const value = Array.isArray(raw) ? raw[0] : raw
  if (typeof value !== 'number' || !Number.isFinite(value)) return
  emit('preview', withValue(spec.key, value))
}

function onSliderCommit(spec: PrototypeTweakSpec, raw: number | [number, number]) {
  const value = Array.isArray(raw) ? raw[0] : raw
  if (typeof value !== 'number' || !Number.isFinite(value)) return
  const next = withValue(spec.key, value)
  emit('preview', next)
  emit('commit', next)
}

function onImmediateChange(
  spec: PrototypeTweakSpec,
  value: PrototypeTweakValue | null | undefined,
) {
  if (value === null || value === undefined) return
  const next = withValue(spec.key, value)
  emit('preview', next)
  emit('commit', next)
}

function onTextPreview(spec: PrototypeTweakSpec, val: string) {
  emit('preview', withValue(spec.key, val))
}

function onTextCommit(spec: PrototypeTweakSpec, val: string) {
  const next = withValue(spec.key, val)
  emit('preview', next)
  emit('commit', next)
}

function formatRangeValue(spec: PrototypeTweakSpec, val: PrototypeTweakValue | undefined): string {
  const n = typeof val === 'number' ? val : Number(spec.default)
  return `${n}${spec.unit ?? ''}`
}

function enumControl(spec: PrototypeTweakSpec): EnumTweakControl {
  return resolveEnumTweakControl(spec.options)
}
</script>

<template>
  <div class="tweaks-panel" data-dou-nodrag @pointerdown.stop @wheel.stop>
    <div class="tweaks-header">
      <DouText weight="medium" size="sm">{{ t('artifact.prototype.tweaksTitle') }}</DouText>
      <DouButton
        v-if="modifiedCount > 0 && !readonly"
        size="sm"
        variant="ghost"
        @click="emit('reset')"
      >
        <template #leading><RotateCcwIcon /></template>
        {{ t('artifact.prototype.tweaksReset') }}
      </DouButton>
    </div>

    <div class="tweaks-body">
      <section v-for="(group, idx) in groups" :key="group.section ?? idx" class="tweaks-section">
        <DouText
          v-if="group.section || groups.length > 1"
          variant="tertiary"
          size="sm"
          weight="medium"
          class="tweaks-section-title"
        >
          {{ group.section ?? t('artifact.prototype.tweaksGeneral') }}
        </DouText>

        <div v-for="spec in group.items" :key="spec.key" class="tweak-row">
          <template v-if="spec.kind === 'boolean'">
            <div class="tweak-inline">
              <DouText size="sm" class="tweak-label">{{ spec.label }}</DouText>
              <DouSwitch
                size="sm"
                :model-value="Boolean(values[spec.key])"
                :disabled="readonly"
                @update:model-value="(val) => onImmediateChange(spec, Boolean(val))"
              />
            </div>
          </template>

          <template v-else-if="spec.kind === 'color'">
            <div class="tweak-inline">
              <DouText size="sm" class="tweak-label">{{ spec.label }}</DouText>
              <DouColorPicker
                size="sm"
                :model-value="String(values[spec.key] ?? spec.default)"
                :presets="spec.swatches ?? []"
                :disabled="readonly"
                show-text
                @update:model-value="
                  (val) => {
                    if (val) emit('preview', withValue(spec.key, val))
                  }
                "
                @change="(val) => onImmediateChange(spec, val)"
              />
            </div>
          </template>

          <template v-else-if="spec.kind === 'range'">
            <div class="tweak-stack">
              <div class="tweak-inline">
                <DouText size="sm" class="tweak-label">{{ spec.label }}</DouText>
                <DouText variant="tertiary" size="sm" class="tweak-value">
                  {{ formatRangeValue(spec, values[spec.key]) }}
                </DouText>
              </div>
              <DouSlider
                size="sm"
                :model-value="Number(values[spec.key] ?? spec.default)"
                :min="spec.min ?? 0"
                :max="spec.max ?? 100"
                :step="spec.step ?? 1"
                :disabled="readonly"
                @update:model-value="(val) => onSliderPreview(spec, val)"
                @change="(val) => onSliderCommit(spec, val)"
              />
            </div>
          </template>

          <template v-else-if="spec.kind === 'enum'">
            <div class="tweak-stack">
              <DouText size="sm" class="tweak-label">{{ spec.label }}</DouText>
              <DouSegmentedControl
                v-if="enumControl(spec) === 'segmented'"
                size="sm"
                block
                :model-value="String(values[spec.key] ?? spec.default)"
                :options="spec.options ?? []"
                :disabled="readonly"
                @update:model-value="(val) => onImmediateChange(spec, String(val))"
              />
              <DouCheckTagGroup
                v-else-if="enumControl(spec) === 'tags'"
                size="sm"
                :model-value="String(values[spec.key] ?? spec.default)"
                :options="spec.options ?? []"
                :disabled="readonly"
                @update:model-value="
                  (val) => {
                    if (typeof val === 'string') onImmediateChange(spec, val)
                  }
                "
              />
              <DouSelect
                v-else
                size="sm"
                :model-value="String(values[spec.key] ?? spec.default)"
                :options="spec.options ?? []"
                :disabled="readonly"
                @update:model-value="
                  (val) => {
                    if (typeof val === 'string') onImmediateChange(spec, val)
                  }
                "
              />
            </div>
          </template>

          <template v-else-if="spec.kind === 'text'">
            <div class="tweak-stack">
              <DouText size="sm" class="tweak-label">{{ spec.label }}</DouText>
              <DouInput
                size="sm"
                :model-value="String(values[spec.key] ?? spec.default)"
                :disabled="readonly"
                @update:model-value="(val) => onTextPreview(spec, String(val))"
                @blur="onTextCommit(spec, String(values[spec.key] ?? spec.default))"
                @keydown.enter="onTextCommit(spec, String(values[spec.key] ?? spec.default))"
              />
            </div>
          </template>
        </div>
      </section>
    </div>
  </div>
</template>

<style scoped>
.tweaks-panel {
  display: flex;
  flex-direction: column;
  gap: 10px;
  width: min(288px, calc(100vw - 32px));
  min-width: 0;
  max-width: 100%;
}

.tweaks-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  min-height: 24px;
}

.tweaks-body {
  display: flex;
  flex-direction: column;
  gap: 12px;
  max-height: 360px;
  overflow-y: auto;
  padding-right: 2px;
}

.tweaks-section {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.tweaks-section + .tweaks-section {
  padding-top: 10px;
  border-top: 1px solid var(--dou-color-border-subtle, var(--dou-color-border));
}

.tweaks-section-title {
  letter-spacing: 0.02em;
}

.tweak-row {
  display: flex;
  flex-direction: column;
}

.tweak-inline {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
}

.tweak-stack {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.tweak-label {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.tweak-value {
  font-variant-numeric: tabular-nums;
}
</style>
