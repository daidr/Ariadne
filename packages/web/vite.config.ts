import vue from '@vitejs/plugin-vue'
import { colorModeScript } from 'dou-ui'
import { defineConfig } from 'vite'
import { VitePWA } from 'vite-plugin-pwa'
import { sharedShikiPlugin } from './shiki-bundle-plugin'
import { COLOR_MODE_KEY, THEME_COLORS, themeColorScript } from './src/color-mode'

export default defineConfig({
  worker: {
    format: 'es',
  },
  plugins: [
    sharedShikiPlugin(),
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
        {
          tag: 'script',
          children: themeColorScript(),
          injectTo: 'head',
        },
      ],
    },
    VitePWA({
      registerType: 'autoUpdate',
      injectRegister: 'auto',
      includeAssets: ['favicon.svg', 'favicon-32x32.png', 'favicon.ico', 'apple-touch-icon.png'],
      manifest: {
        id: '/',
        name: 'Ariadne',
        short_name: 'Ariadne',
        lang: 'zh-CN',
        start_url: '/',
        scope: '/',
        display: 'standalone',
        background_color: THEME_COLORS.light,
        theme_color: THEME_COLORS.light,
        icons: [
          {
            src: '/icons/icon-192.png',
            sizes: '192x192',
            type: 'image/png',
            purpose: 'any',
          },
          {
            src: '/icons/icon-512.png',
            sizes: '512x512',
            type: 'image/png',
            purpose: 'any',
          },
          {
            src: '/icons/icon-maskable-512.png',
            sizes: '512x512',
            type: 'image/png',
            purpose: 'maskable',
          },
        ],
      },
      workbox: {
        cleanupOutdatedCaches: true,
        clientsClaim: true,
        skipWaiting: true,
        // Precache only includeAssets + manifest icons instead of all lazy grammar/editor chunks.
        globPatterns: [],
        // Keep navigation network-first so Go binary self-updates take effect immediately,
        // while bypassing /api/* (SSE, WebSockets, cookie-authenticated files) completely.
        navigateFallback: null,
        runtimeCaching: [
          {
            urlPattern: ({ request, url, sameOrigin }) =>
              sameOrigin && request.mode === 'navigate' && !url.pathname.startsWith('/api/'),
            handler: 'NetworkFirst',
            options: {
              cacheName: 'ariadne-pages',
              networkTimeoutSeconds: 5,
              cacheableResponse: {
                statuses: [200],
              },
              expiration: {
                maxEntries: 20,
                maxAgeSeconds: 7 * 24 * 60 * 60,
              },
            },
          },
          {
            urlPattern: ({ url, sameOrigin }) => sameOrigin && url.pathname.startsWith('/assets/'),
            handler: 'CacheFirst',
            options: {
              cacheName: 'ariadne-assets',
              cacheableResponse: {
                statuses: [200],
              },
              expiration: {
                maxEntries: 200,
                maxAgeSeconds: 30 * 24 * 60 * 60,
              },
            },
          },
        ],
      },
    }),
  ],
  server: {
    port: 5173,
    // The local server owns the API; Vite only serves the app during development.
    // `ws`: documents and whiteboards are edited together over a WebSocket.
    proxy: {
      '/api': { target: `http://127.0.0.1:${process.env.ARIADNE_PORT ?? 7428}`, ws: true },
    },
  },
})
