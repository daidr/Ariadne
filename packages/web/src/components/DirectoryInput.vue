<script setup lang="ts">
import { DouAutocomplete, DouText, type AutocompleteOption } from 'dou-ui'
import { FolderIcon, FolderUpIcon } from '@dou-ui/icons'
import { ref } from 'vue'
import { ApiError, type DirectoryListing } from '../api'
import { tm } from '../i18n'

// A directory path typed in, with the directories at what is typed listed under it: the
// one above, then the ones inside (or, part way through a name, those starting like it).
const model = defineModel<string>({ default: '' })
const props = defineProps<{
  browse: (typed: string) => Promise<DirectoryListing>
  placeholder?: string | undefined
  size?: 'sm' | 'md' | 'lg'
  ariaLabel?: string | undefined
}>()

const PARENT = '..'
const problem = ref<string | null>(null)

async function suggest(typed: string): Promise<AutocompleteOption[]> {
  try {
    const listing = await props.browse(typed)
    problem.value = null
    return [
      ...(listing.parent ? [{ value: listing.parent, label: PARENT }] : []),
      ...listing.directories.map((entry) => ({ value: entry.path, label: entry.name })),
    ]
  } catch (cause) {
    problem.value = cause instanceof ApiError ? tm(cause.localized) : String(cause)
    return []
  }
}
</script>

<template>
  <DouAutocomplete
    v-model="model"
    :fetch="suggest"
    :placeholder="placeholder"
    :size="size"
    :aria-label="ariaLabel"
    spellcheck="false"
  >
    <template #option="{ option }">
      <span class="directory-option">
        <FolderUpIcon v-if="option.label === PARENT" aria-hidden="true" />
        <FolderIcon v-else aria-hidden="true" />
        <span class="directory-name">{{ option.label }}</span>
      </span>
    </template>
    <template v-if="problem" #empty>
      <DouText size="sm" tone="danger">{{ problem }}</DouText>
    </template>
  </DouAutocomplete>
</template>

<style scoped>
.directory-option {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.directory-option > svg {
  flex-shrink: 0;
  color: var(--dou-color-text-tertiary);
}

.directory-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
