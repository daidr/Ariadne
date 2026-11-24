import fs from 'node:fs'
import path from 'node:path'
import zlib from 'node:zlib'
import vue from '@vitejs/plugin-vue'
import { colorModeScript } from 'dou-ui'
import type { Plugin } from 'vite'
import { defineConfig } from 'vite'
import { VitePWA } from 'vite-plugin-pwa'
import { douUiOnDemandStylesPlugin } from './dou-ui-style-plugin.ts'
import { sharedShikiPlugin } from './shiki-bundle-plugin.ts'
import { COLOR_MODE_KEY, THEME_COLORS, themeColorScript } from './src/color-mode.ts'

const PRECOMPRESS_MIN_BYTES = 256
const PRECOMPRESS_TEXT_EXTS = new Set([
  '.html',
  '.htm',
  '.js',
  '.mjs',
  '.css',
  '.svg',
  '.json',
  '.webmanifest',
  '.txt',
  '.xml',
])
const PRECOMPRESS_GENERIC_EXTS = new Set(['.wasm'])

function collectFiles(dir: string, out: string[] = []): string[] {
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const fullPath = path.join(dir, entry.name)
    if (entry.isDirectory()) {
      collectFiles(fullPath, out)
    } else if (entry.isFile()) {
      out.push(fullPath)
    }
  }
  return out
}

function precompressStaticAssets(): Plugin {
  let outDir = ''
  let isBuild = false
  return {
    name: 'ariadne-precompress',
    apply: 'build',
    configResolved(config) {
      isBuild = config.command === 'build'
      outDir = path.resolve(config.root, config.build.outDir)
    },
    closeBundle: {
      order: 'post',
      handler() {
        if (!isBuild || !outDir || !fs.existsSync(outDir)) return
        for (const filePath of collectFiles(outDir)) {
          const ext = path.extname(filePath).toLowerCase()
          const isText = PRECOMPRESS_TEXT_EXTS.has(ext)
          const isGeneric = PRECOMPRESS_GENERIC_EXTS.has(ext)
          if (!isText && !isGeneric) continue
          const raw = fs.readFileSync(filePath)
          if (raw.length < PRECOMPRESS_MIN_BYTES) continue
          const compressed = zlib.brotliCompressSync(raw, {
            params: {
              [zlib.constants.BROTLI_PARAM_QUALITY]: 9,
              [zlib.constants.BROTLI_PARAM_MODE]: isText
                ? zlib.constants.BROTLI_MODE_TEXT
                : zlib.constants.BROTLI_MODE_GENERIC,
              [zlib.constants.BROTLI_PARAM_SIZE_HINT]: raw.length,
            },
          })
          if (compressed.length < raw.length) {
            fs.writeFileSync(`${filePath}.br`, compressed)
          }
        }
      },
    },
  }
}

export default defineConfig({
  worker: {
    format: 'es',
  },
  build: {
    rolldownOptions: {
      output: {
        codeSplitting: {
          groups: [
            {
              name: 'vendor-vue',
              tags: ['$initial'],
              test: /[\\/]node_modules[\\/].*(?:vue|@vue[\\/]|vue-router)[\\/]/,
              priority: 40,
            },
            {
              name: 'vendor-rpc',
              tags: ['$initial'],
              test: /[\\/]node_modules[\\/].*(?:@bufbuild[\\/]|@connectrpc[\\/])|[\\/]packages[\\/]proto[\\/]/,
              priority: 35,
            },
            {
              name: 'vendor-dou-ui',
              tags: ['$initial'],
              test: /[\\/]node_modules[\\/].*(?:dou-ui|@dou-ui[\\/])/,
              priority: 30,
            },
            {
              name: 'app-shared',
              tags: ['$initial'],
              minShareCount: 2,
              priority: 10,
            },
          ],
        },
      },
    },
  },
  plugins: [
    sharedShikiPlugin(),
    vue(),
    douUiOnDemandStylesPlugin(),
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
    precompressStaticAssets(),
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
