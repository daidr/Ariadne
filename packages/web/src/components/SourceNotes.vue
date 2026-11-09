<script setup lang="ts">
import { DouText } from 'dou-ui'
import { t } from '../i18n'
import { useSourceStatus } from '../source-status'

/** Where commands run, when the project's source is on another machine. */
const { source, remote, remoteHost } = useSourceStatus()
</script>

<template>
  <DouText v-if="source && remote" variant="tertiary" size="sm">
    {{ t('source.remote', { host: remoteHost }) }}
  </DouText>
</template>
