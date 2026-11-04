<script setup lang="ts">
import { DouButton, DouInput } from 'dou-ui'
import { CloseIcon, PlusIcon } from '@dou-ui/icons'
import { t } from '../i18n'

/** Name and value pairs whose values may be secret: a stored one shows only its name. */
export interface DraftPair {
  name: string
  /** Empty keeps the stored value, when `stored`. */
  value: string
  stored: boolean
}

const pairs = defineModel<DraftPair[]>({ required: true })
defineProps<{ addLabel: string }>()
</script>

<script lang="ts">
export const storedPairs = (names: string[]): DraftPair[] =>
  names.map((name) => ({ name, value: '', stored: true }))

/** Pairs with a name, as the server takes them; an empty value keeps the stored one. */
export const pairsInput = (list: DraftPair[]) =>
  list
    .filter((pair) => pair.name.trim())
    .map((pair) => ({ name: pair.name.trim(), value: pair.value }))
</script>

<template>
  <div class="pairs">
    <div v-for="(pair, at) in pairs" :key="at" class="pairs-row">
      <DouInput
        v-model="pair.name"
        class="pairs-name"
        :readonly="pair.stored"
        :aria-label="t('mcp.pairName')"
        :placeholder="t('mcp.pairName')"
      />
      <DouInput
        v-model="pair.value"
        type="password"
        show-password
        class="pairs-value"
        :aria-label="t('mcp.pairValue')"
        :placeholder="pair.stored ? t('settings.secretSet') : t('mcp.pairValue')"
      />
      <DouButton
        size="sm"
        variant="ghost"
        :aria-label="t('settings.remove')"
        :title="t('settings.remove')"
        @click="pairs.splice(at, 1)"
      >
        <CloseIcon />
      </DouButton>
    </div>
    <DouButton
      size="sm"
      variant="ghost"
      class="pairs-add"
      @click="pairs.push({ name: '', value: '', stored: false })"
    >
      <template #leading><PlusIcon /></template>
      {{ addLabel }}
    </DouButton>
  </div>
</template>

<style scoped>
.pairs {
  display: flex;
  flex-direction: column;
  gap: 6px;
  width: 100%;
}

.pairs-row {
  display: flex;
  align-items: center;
  gap: 8px;
}

.pairs-name {
  flex: none;
  width: 180px;
}

.pairs-value {
  flex: 1;
  min-width: 0;
}

.pairs-add {
  align-self: flex-start;
}
</style>
