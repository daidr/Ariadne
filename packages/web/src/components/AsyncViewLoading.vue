<script setup lang="ts">
import { DouSpinner } from 'dou-ui'
</script>

<template>
  <div class="async-view-loading" role="status" aria-busy="true">
    <DouSpinner />
  </div>
</template>

<style scoped>
.async-view-loading {
  display: grid;
  place-items: center;
  width: 100%;
  height: 100%;
  min-height: 120px;
}
</style>
