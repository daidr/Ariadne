<script setup lang="ts">
import { useId } from 'vue'

const maskId = useId()
</script>

<template>
  <svg class="ariadne-logo" viewBox="0 0 64 64" fill="none" aria-hidden="true">
    <defs>
      <mask :id="maskId">
        <rect width="64" height="64" fill="#ffffff" />
        <g transform="translate(32 35) rotate(-16)">
          <path d="M -22 -5.8 H 13.4 L 23.4 0 L 13.4 5.8 H -22 Z" fill="#000000" />
        </g>
      </mask>
    </defs>
    <path
      class="ariadne-logo-ink"
      d="M 17.5 50.5 L 30.5 16 Q 32 12.2 33.5 16 L 46.5 50.5"
      fill="none"
      stroke-width="5.8"
      stroke-linecap="round"
      stroke-linejoin="round"
      :mask="`url(#${maskId})`"
    />
    <g transform="translate(32 35) rotate(-16)">
      <path
        class="ariadne-logo-cap"
        d="M -17.4 -3.4 H -16 V 3.4 H -17.4 A 2.6 2.6 0 0 1 -20 0.8 V -0.8 A 2.6 2.6 0 0 1 -17.4 -3.4 Z"
      />
      <rect class="ariadne-logo-body" x="-14" y="-3.4" width="25.4" height="6.8" rx="0.5" />
      <path class="ariadne-logo-body" d="M 13.4 -3.35 L 21.2 0 L 13.4 3.35 Z" />
    </g>
  </svg>
</template>

<style scoped>
.ariadne-logo {
  display: block;
  flex: none;
  width: 20px;
  height: 20px;
}

.ariadne-logo-ink {
  stroke: var(--dou-color-text);
}

.ariadne-logo-body {
  fill: var(--dou-color-text);
}

.ariadne-logo-cap {
  fill: var(--dou-color-text-tertiary);
}
</style>
