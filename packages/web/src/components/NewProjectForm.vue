<script setup lang="ts">
import { DouForm, DouFormItem, DouInput, DouText } from 'dou-ui'
import { computed, ref, useTemplateRef, watch } from 'vue'
import { t } from '../i18n'
import { offerNotifications } from '../notify'
import { focusCoordinator } from '../shortcuts'
import { startProject } from '../store'
import FileSourcesForm from './FileSourcesForm.vue'

const sources = useTemplateRef<InstanceType<typeof FileSourcesForm>>('sources')
const name = ref('')
const named = ref(false)
const creating = ref(false)
const problem = ref<string | null>(null)
const ready = computed(() => !!name.value.trim() && !!sources.value?.ready)

watch(
  () => sources.value?.primaryName ?? '',
  (suggested) => {
    if (!named.value) name.value = suggested
  },
)

function rename(value: string) {
  name.value = value
  named.value = !!value.trim()
}

function onEnter(event: KeyboardEvent) {
  if (event.defaultPrevented || event.isComposing || !(event.target instanceof HTMLInputElement))
    return
  event.preventDefault()
  if (sources.value?.editing) {
    sources.value.confirm()
    return
  }
  void create()
}

async function create() {
  if (!ready.value || creating.value || !sources.value) return
  creating.value = true
  problem.value = null
  try {
    problem.value = await startProject({ name: name.value.trim(), ...sources.value.input() })
    if (problem.value) return
    offerNotifications()
    void focusCoordinator()
  } finally {
    creating.value = false
  }
}

defineExpose({ create, ready, creating })
</script>

<template>
  <DouForm layout="vertical" size="sm" class="new-project" @keydown.enter="onEnter" @submit.prevent>
    <DouFormItem :label="t('newProject.name')" required>
      <DouInput :model-value="name" @update:model-value="rename" />
    </DouFormItem>
    <FileSourcesForm ref="sources" />
    <DouText v-if="problem" tone="danger" size="sm" role="alert" class="new-project-problem">
      {{ problem }}
    </DouText>
  </DouForm>
</template>

<style scoped>
.new-project {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.new-project-problem {
  overflow-wrap: anywhere;
}
</style>
