import vue from '@vitejs/plugin-vue'
import { colorModeScript } from 'dou-ui'
import { defineConfig } from 'vite'
import { COLOR_MODE_KEY } from './src/color-mode'

export default defineConfig({
  plugins: [
    vue(),
    {
      // Write data-dou-theme before the first paint, so dark mode does not flash light.
      name: 'ariadne-color-mode',
      transformIndexHtml: () => [
        {
          tag: 'script',
          children: colorModeScript({ storageKey: COLOR_MODE_KEY }),
          injectTo: 'head-prepend',
        },
      ],
    },
  ],
  server: {
    port: 5173,
    // The local server owns the API; Vite only serves the app during development.
    proxy: { '/api': `http://127.0.0.1:${process.env.ARIADNE_PORT ?? 7428}` },
  },
})
