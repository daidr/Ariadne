<script setup lang="ts">
import { state } from '../store'
import SidePane from './SidePane.vue'

/** The right side, beside the coordinator: the project's lists and threads, split as the user likes. */
</script>

<template>
  <div class="side-layout">
    <SidePane :node="state.side.root" />
  </div>
</template>

<style scoped>
.side-layout {
  height: 100%;
  min-height: 0;
  min-width: 0;
}
</style>
