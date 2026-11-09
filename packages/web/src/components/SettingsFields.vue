<script setup lang="ts">
import { DouButton, DouFormItem, DouInput, DouInputNumber, DouSelect, DouSwitch } from 'dou-ui'
import type { SettingsSchema } from '@ariadne/plugin-sdk'
import type { DirectoryListing } from '../api'
import { lt, t } from '../i18n'
import DirectoryInput from './DirectoryInput.vue'

// The form a plugin describes with its settings schema. Values are edited in place.
// A stored secret is never sent to the browser: it shows as "set" and can be cleared.
const props = defineProps<{
  schema: SettingsSchema
  values: Record<string, unknown>
  secretsSet: string[]
  /** Lists directories for `directory` settings; without it they are only typed. */
  browse?: ((key: string, typed: string) => Promise<DirectoryListing | null>) | undefined
  /** Without the fields' descriptions, where they are written for another use of the kind. */
  bare?: boolean
}>()
const emit = defineEmits<{ clearSecret: [key: string] }>()

const asString = (value: unknown) => (typeof value === 'string' ? value : '')
const asNumber = (value: unknown) => (typeof value === 'number' ? value : null)
const isSet = (key: string | number) => props.secretsSet.includes(String(key))
</script>

<template>
  <template v-for="(field, key) in schema.properties" :key="key">
    <DouFormItem
      :label="lt(field.title)"
      :required="schema.required?.includes(String(key))"
      :help="
        field.type === 'string' && field.secret && isSet(key)
          ? t('settings.secretSet')
          : bare
            ? undefined
            : lt(field.description)
      "
    >
      <DirectoryInput
        v-if="field.type === 'string' && field.directory && browse"
        :model-value="asString(values[key])"
        :placeholder="field.default"
        :browse="(typed) => browse!(String(key), typed)"
        @update:model-value="(value) => (values[key] = value)"
      />
      <DouInput
        v-else-if="field.type === 'string'"
        :model-value="asString(values[key])"
        :type="field.secret ? 'password' : 'text'"
        :show-password="field.secret"
        :placeholder="field.default"
        @update:model-value="(value) => (values[key] = value)"
      >
        <template v-if="field.secret && isSet(key)" #suffix>
          <DouButton size="sm" variant="ghost" @click="emit('clearSecret', String(key))">
            {{ t('settings.clearSecret') }}
          </DouButton>
        </template>
      </DouInput>
      <DouInputNumber
        v-else-if="field.type === 'number'"
        :model-value="asNumber(values[key])"
        :placeholder="field.default === undefined ? undefined : String(field.default)"
        @update:model-value="(value) => (values[key] = value)"
      />
      <DouSwitch
        v-else-if="field.type === 'boolean'"
        :model-value="values[key] === true"
        @update:model-value="(value) => (values[key] = value)"
      />
      <DouSelect
        v-else
        :model-value="asString(values[key]) || field.default || null"
        :options="field.options"
        @update:model-value="(value) => (values[key] = value)"
      />
    </DouFormItem>
  </template>
</template>
