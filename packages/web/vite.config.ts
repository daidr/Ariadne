import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vite'

export default defineConfig({
  plugins: [vue()],
  server: {
    port: 5173,
    // The local server owns the API; Vite only serves the app during development.
    proxy: { '/api': `http://127.0.0.1:${process.env.ARIADNE_PORT ?? 7428}` },
  },
})
