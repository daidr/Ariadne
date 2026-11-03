import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vitest/config'

// Run with `bun --bun vitest` so server-side tests get Bun's runtime (bun:sqlite, Bun.serve).
export default defineConfig({
  plugins: [vue()],
  test: {
    include: ['packages/*/src/**/*.test.ts'],
  },
})
