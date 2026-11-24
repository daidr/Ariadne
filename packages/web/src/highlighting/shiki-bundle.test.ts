import { posix, resolve } from 'node:path'
import { describe, expect, test } from 'vitest'
import { buildSharedShikiBundle } from '../../shiki-bundle-plugin'
import { shikiWorkerUrl } from './shiki-worker-url'

const webRoot = resolve(import.meta.dirname, '../..')

describe('shared Shiki bundle deduplication', () => {
  test('shikiWorkerUrl points to shiki.worker.ts in dev/test environments', () => {
    const url = shikiWorkerUrl()
    expect(url).toBeInstanceOf(URL)
    expect(url.pathname.endsWith('/shiki.worker.ts')).toBe(true)
  })

  test('builds one shared worker-safe Shiki runtime, standalone WASM, and deduplicated grammars', async () => {
    const bundle = await buildSharedShikiBundle({ root: webRoot })
    const byName = new Map(bundle.emittedFiles.map((file) => [file.fileName, file.source]))

    const workerSource = byName.get(bundle.workerChunkFile)
    const shikiEntrySource = byName.get(bundle.shikiChunkFile)
    expect(typeof workerSource).toBe('string')
    expect(typeof shikiEntrySource).toBe('string')

    // Worker entry stays a thin wrapper importing from the shared Shiki chunk instead of
    // duplicating the Shiki engine or grammar files.
    expect((workerSource as string).length).toBeLessThan(2048)
    expect(workerSource as string).toMatch(/import\{[^}]+\}from"\.\/shiki-[^"]+\.js"/)

    // Shared Shiki entry exports both main-thread and worker APIs.
    expect(shikiEntrySource as string).toContain('highlightCodeToHtml')
    expect(shikiEntrySource as string).toContain('loadShikiLanguage')

    // WASM is emitted as a standalone binary asset instead of base64-inlined into JS.
    const wasmFiles = bundle.emittedFiles.filter((file) => file.fileName.endsWith('.wasm'))
    expect(wasmFiles).toHaveLength(1)
    expect(wasmFiles[0]!.source).toBeInstanceOf(Uint8Array)

    // No emitted Shiki JS chunk uses DOM-only Vite preload helpers (must stay Web Worker-safe).
    const jsChunks = bundle.emittedFiles.filter((file) => file.fileName.endsWith('.js'))
    for (const chunk of jsChunks) {
      const code = chunk.source as string
      expect(code, `${chunk.fileName} must not use __vitePreload`).not.toContain('__vitePreload')
      expect(code, `${chunk.fileName} must not inline base64 WASM`).not.toContain(
        'data:application/wasm',
      )
    }

    // Previously duplicated embedded-language grammars appear at most once (>100B payload chunk).
    const largeByPrefix = new Map<string, string[]>()
    for (const chunk of jsChunks) {
      if ((chunk.source as string).length <= 100) continue
      const base = posix.basename(chunk.fileName)
      const match = base.match(/^([a-z0-9-]+)-[A-Za-z0-9_-]+\.js$/)
      if (!match) continue
      const prefix = match[1]!
      const list = largeByPrefix.get(prefix) ?? []
      list.push(chunk.fileName)
      largeByPrefix.set(prefix, list)
    }

    for (const grammar of ['cpp', 'php', 'html', 'ruby', 'scss', 'vue', 'graphql', 'lua', 'xml']) {
      expect(
        largeByPrefix.get(grammar),
        `expected a single chunk for grammar ${grammar}`,
      ).toHaveLength(1)
    }
  }, 20_000)
})
