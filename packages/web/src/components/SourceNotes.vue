<script setup lang="ts">
import { DouAlert, DouText } from 'dou-ui'
import { t } from '../i18n'
import { useSourceStatus } from '../source-status'

/** What the user should know about how commands run on the project's source. */
const { source, sandbox, remote, remoteHost, unsandboxed, weaker, sandboxNote } = useSourceStatus()
</script>

<template>
  <DouAlert
    v-if="unsandboxed && sandbox"
    tone="warning"
    role="alert"
    :title="t(sandbox.mode === 'off' ? 'sandbox.off.title' : 'sandbox.missing.title')"
  >
    {{ t(sandboxNote) }}
  </DouAlert>
  <DouText v-else-if="weaker" variant="tertiary" size="sm">{{ t('sandbox.weaker') }}</DouText>
  <DouText v-else-if="source && remote" variant="tertiary" size="sm">
    {{ t('sandbox.remote', { host: remoteHost }) }}
  </DouText>
</template>
