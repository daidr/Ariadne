import { readFile } from 'node:fs/promises'
import { posix, resolve } from 'node:path'
import { build as viteBuild, normalizePath, type Plugin, type ResolvedConfig } from 'vite'

export interface SharedShikiBundle {
  emittedFiles: Array<{ fileName: string; source: string | Uint8Array }>
  shikiChunkFile: string
  workerChunkFile: string
  codeWorkerChunkFile: string
}

export interface BuildSharedShikiOptions {
  root: string
  base?: string
  assetsDir?: string
  target?: ResolvedConfig['build']['target']
  minify?: ResolvedConfig['build']['minify']
  sourcemap?: ResolvedConfig['build']['sourcemap']
}

/**
 * Build `shiki.ts`, `shiki.worker.ts` and `code.worker.ts` together in one pure-ESM Vite build
 * (no DOM-only `__vitePreload` helper, full minification, standalone `onig.wasm` asset) so the
 * main thread fallback, the Shiki HTML worker and the `@dou-ui/code` syntax/diff worker share
 * the exact same runtime, WASM, themes and grammar chunks.
 */
export async function buildSharedShikiBundle(
  options: BuildSharedShikiOptions,
): Promise<SharedShikiBundle> {
  const dir = resolve(options.root, 'src/highlighting')
  const shikiEntryPath = normalizePath(resolve(dir, 'shiki.ts'))
  const workerEntryPath = normalizePath(resolve(dir, 'shiki.worker.ts'))
  const codeWorkerEntryPath = normalizePath(resolve(dir, 'code.worker.ts'))
  const assetsDir = options.assetsDir ?? 'assets'

  const result = await viteBuild({
    configFile: false,
    root: options.root,
    base: options.base ?? '/',
    logLevel: 'silent',
    plugins: [
      {
        // Keep `onig.wasm?url` as a separate binary asset instead of lib-mode base64 inlining.
        name: 'ariadne-shiki-wasm-asset',
        enforce: 'pre',
        async load(id) {
          if (!id.endsWith('.wasm?url')) return null
          const filePath = id.slice(0, -'?url'.length)
          const ref = this.emitFile({
            type: 'asset',
            name: posix.basename(normalizePath(filePath)),
            source: await readFile(filePath),
          })
          return `export default import.meta.ROLLUP_FILE_URL_${ref}\n`
        },
      },
    ],
    build: {
      write: false,
      emptyOutDir: false,
      assetsDir,
      target: options.target,
      minify: options.minify,
      sourcemap: options.sourcemap,
      lib: {
        entry: {
          shiki: shikiEntryPath,
          'shiki.worker': workerEntryPath,
          'code.worker': codeWorkerEntryPath,
        },
        formats: ['es'],
      },
      rolldownOptions: {
        output: {
          minify: options.minify !== false,
          entryFileNames: `${assetsDir}/[name]-[hash].js`,
          chunkFileNames: `${assetsDir}/[name]-[hash].js`,
          assetFileNames: `${assetsDir}/[name]-[hash].[ext]`,
        },
      },
    },
  })

  const outputs = Array.isArray(result) ? result : [result]
  const emittedFiles: Array<{ fileName: string; source: string | Uint8Array }> = []
  let shikiChunkFile = ''
  let workerChunkFile = ''
  let codeWorkerChunkFile = ''

  for (const bundle of outputs) {
    if (!('output' in bundle)) continue
    for (const item of bundle.output) {
      if (item.type === 'chunk') {
        emittedFiles.push({ fileName: item.fileName, source: item.code })
        if (item.isEntry && item.name === 'shiki') {
          shikiChunkFile = item.fileName
        } else if (item.isEntry && item.name === 'shiki.worker') {
          workerChunkFile = item.fileName
        } else if (item.isEntry && item.name === 'code.worker') {
          codeWorkerChunkFile = item.fileName
        }
      } else {
        emittedFiles.push({ fileName: item.fileName, source: item.source })
      }
    }
  }

  if (!shikiChunkFile || !workerChunkFile || !codeWorkerChunkFile) {
    throw new Error(
      `ariadne-shared-shiki failed to locate entry chunks (shiki=${shikiChunkFile}, worker=${workerChunkFile}, codeWorker=${codeWorkerChunkFile})`,
    )
  }

  return { emittedFiles, shikiChunkFile, workerChunkFile, codeWorkerChunkFile }
}

/**
 * Vite build plugin that wires the main client bundle to the shared pure-ESM Shiki bundle,
 * eliminating duplicate Shiki runtime and grammar chunks between the main thread and Worker.
 */
export function sharedShikiPlugin(): Plugin {
  let resolvedConfig: ResolvedConfig | undefined
  let highlightingDir = ''
  let shikiEntryPath = ''
  let workerUrlModulePath = ''
  let codeWorkerUrlModulePath = ''
  let built: SharedShikiBundle | null = null

  return {
    name: 'ariadne-shared-shiki',
    apply: 'build',
    enforce: 'pre',
    configResolved(config) {
      resolvedConfig = config
      highlightingDir = normalizePath(resolve(config.root, 'src/highlighting'))
      shikiEntryPath = normalizePath(resolve(highlightingDir, 'shiki.ts'))
      workerUrlModulePath = normalizePath(resolve(highlightingDir, 'shiki-worker-url.ts'))
      codeWorkerUrlModulePath = normalizePath(resolve(highlightingDir, 'code-worker-url.ts'))
    },
    watchChange(file) {
      if (normalizePath(file).startsWith(`${highlightingDir}/`)) {
        built = null
      }
    },
    async buildStart() {
      if (built || !resolvedConfig || resolvedConfig.isWorker) return
      built = await buildSharedShikiBundle({
        root: resolvedConfig.root,
        base: resolvedConfig.base,
        assetsDir: resolvedConfig.build.assetsDir,
        target: resolvedConfig.build.target,
        minify: resolvedConfig.build.minify,
        sourcemap: resolvedConfig.build.sourcemap,
      })
    },
    async resolveId(source, importer, options) {
      if (!built || !importer || resolvedConfig?.isWorker) return null
      if (!/(?:^|\/)shiki(?:\.ts)?$/.test(source)) return null
      const resolved = await this.resolve(source, importer, { ...options, skipSelf: true })
      if (resolved && normalizePath(resolved.id) === shikiEntryPath) {
        return { id: `./${posix.basename(built.shikiChunkFile)}`, external: true }
      }
      return null
    },
    load(id) {
      if (!built || resolvedConfig?.isWorker) return null
      const normalized = normalizePath(id)
      if (normalized === workerUrlModulePath) {
        const workerRelative = `./${posix.basename(built.workerChunkFile)}`
        return `export function shikiWorkerUrl() { return new URL(/* @vite-ignore */ ${JSON.stringify(workerRelative)}, import.meta.url) }\n`
      }
      if (normalized === codeWorkerUrlModulePath) {
        const workerRelative = `./${posix.basename(built.codeWorkerChunkFile)}`
        return `export function codeWorkerUrl() { return new URL(/* @vite-ignore */ ${JSON.stringify(workerRelative)}, import.meta.url) }\n`
      }
      return null
    },
    generateBundle(_options, bundle) {
      if (!built || resolvedConfig?.isWorker) return
      for (const file of built.emittedFiles) {
        if (file.fileName in bundle) continue
        this.emitFile({
          type: 'asset',
          fileName: file.fileName,
          source: file.source,
        })
      }
    },
  }
}
