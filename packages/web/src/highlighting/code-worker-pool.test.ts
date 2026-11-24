import type { CodeLanguage } from '@dou-ui/code'
import type { WorkerMessageScope } from '@dou-ui/code/worker'
import { createJavaScriptRegexEngine } from 'shiki/engine/javascript'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { effectScope, ref } from 'vue'
import { resetCodeLanguageCache, useCodeLanguage } from './code-language'
import {
  canUseCodeWorker,
  codeEditorWorker,
  codeWorkerPool,
  createAriadneCodeWorkerPool,
  createDefaultCodeWorker,
  resetCodeWorkerAvailability,
} from './code-worker-pool'
import { attachCodeWorker } from './code-worker-runtime'
import { createShikiService } from './shiki'

class FakeWorkerScope implements WorkerMessageScope {
  readonly posted: unknown[] = []
  private readonly listeners = new Set<(event: MessageEvent) => void>()

  postMessage(message: unknown): void {
    this.posted.push(message)
  }

  addEventListener(type: 'message', listener: (event: MessageEvent) => void): void {
    if (type === 'message') this.listeners.add(listener)
  }

  send(data: unknown): void {
    const event = { data } as MessageEvent
    for (const listener of this.listeners) listener(event)
  }
}

describe('CodeEditor / CodeDiff worker pool and worker runtime', () => {
  afterEach(() => {
    resetCodeWorkerAvailability()
    resetCodeLanguageCache()
    vi.unstubAllGlobals()
  })

  test('creates a native @dou-ui/code lazy worker pool handle', () => {
    const factory = vi.fn(() => ({}) as Worker)
    const pool = createAriadneCodeWorkerPool({ maxWorkers: 2, idleTimeout: 0 }, factory)

    expect(pool.kind).toBe('pool')
    expect(pool.maxWorkers).toBe(2)
    expect(pool.size).toBe(0)
    expect(factory).not.toHaveBeenCalled()

    pool.dispose()
  })

  test('marks workers unavailable and falls back to main-thread Shiki when new Worker throws synchronously', async () => {
    vi.stubGlobal(
      'Worker',
      vi.fn(function ThrowingWorker() {
        throw new DOMException('Blocked by CSP worker-src', 'SecurityError')
      }),
    )

    expect(canUseCodeWorker('production')).toBe(true)
    expect(codeEditorWorker('production')).toBe(codeWorkerPool)

    const fakeGo = { name: 'go' } as CodeLanguage
    const loader = vi.fn(async () => fakeGo)
    const path = ref('internal/core/server.go')
    const scope = effectScope()
    const binding = scope.run(() =>
      useCodeLanguage(() => path.value, {
        loader,
        remote: () => canUseCodeWorker('production'),
      }),
    )!

    expect(binding.language.value?.workerMode).toBe('tokens')
    expect(loader).not.toHaveBeenCalled()

    expect(() => createDefaultCodeWorker()).toThrow('Blocked by CSP worker-src')
    expect(canUseCodeWorker('production')).toBe(false)
    expect(codeEditorWorker('production')).toBe(false)

    await vi.waitFor(() => {
      expect(binding.language.value).toBe(fakeGo)
    })
    expect(loader).toHaveBeenCalledTimes(1)

    scope.stop()
  })

  test('marks workers unavailable and falls back to main-thread Shiki when a worker emits an error event', async () => {
    const errorListeners = new Set<() => void>()
    vi.stubGlobal(
      'Worker',
      vi.fn(function StubWorker() {
        return {
          addEventListener(type: string, listener: () => void) {
            if (type === 'error') errorListeners.add(listener)
          },
        }
      }),
    )

    expect(canUseCodeWorker('production')).toBe(true)
    expect(codeEditorWorker('production')).toBe(codeWorkerPool)

    const fakeGo = { name: 'go' } as CodeLanguage
    const loader = vi.fn(async () => fakeGo)
    const path = ref('internal/core/server.go')
    const scope = effectScope()
    const binding = scope.run(() =>
      useCodeLanguage(() => path.value, {
        loader,
        remote: () => canUseCodeWorker('production'),
      }),
    )!

    expect(binding.language.value?.workerMode).toBe('tokens')
    createDefaultCodeWorker()
    expect(canUseCodeWorker('production')).toBe(true)

    for (const listener of Array.from(errorListeners)) listener()

    expect(canUseCodeWorker('production')).toBe(false)
    expect(codeEditorWorker('production')).toBe(false)

    await vi.waitFor(() => {
      expect(binding.language.value).toBe(fakeGo)
    })
    expect(loader).toHaveBeenCalledTimes(1)

    scope.stop()
  })

  test('code worker resolves Shiki languages lazily in flat tokens mode and handles async diff', async () => {
    const shiki = createShikiService(() => createJavaScriptRegexEngine())
    const scope = new FakeWorkerScope()
    attachCodeWorker(scope, shiki.loadLanguage)

    // Built-in language works synchronously via checkpoint states protocol.
    scope.send({ t: 'open', id: 1, language: 'typescript', nested: [], tabSize: 2, mode: 'auto' })
    scope.send({ t: 'text', id: 1, lines: ['export const x = 1'], done: true })
    scope.send({ t: 'read', id: 1, from: 0, until: 1, seq: 1 })

    await vi.waitFor(() => {
      const statesMsg = scope.posted.find(
        (m): m is { t: string; id: number } =>
          typeof m === 'object' &&
          m !== null &&
          't' in m &&
          m.t === 'states' &&
          'id' in m &&
          m.id === 1,
      )
      expect(statesMsg).toBeDefined()
    })

    // Shiki language whose canonical key differs from Shiki grammar name (dockerfile -> docker)
    // resolves lazily and replies with flat per-line token arrays.
    scope.send({ t: 'open', id: 2, language: 'dockerfile', nested: [], tabSize: 2, mode: 'tokens' })
    scope.send({
      t: 'text',
      id: 2,
      lines: ['FROM golang:1.24-alpine', 'RUN go build ./...'],
      done: true,
    })
    scope.send({ t: 'read', id: 2, from: 0, until: 2, tokensFrom: 1, tokensTo: 2, seq: 1 })

    await vi.waitFor(() => {
      const tokensMsg = scope.posted.find(
        (m): m is { t: string; id: number; lines: number[][] } =>
          typeof m === 'object' &&
          m !== null &&
          't' in m &&
          m.t === 'tokens' &&
          'id' in m &&
          m.id === 2,
      )
      expect(tokensMsg).toBeDefined()
      expect(tokensMsg!.lines).toHaveLength(2)
      expect(tokensMsg!.lines[0]!.length).toBeGreaterThan(0)
    })

    // Async diffText computation inside the same worker.
    scope.send({ t: 'diff', id: 9, original: 'a\nb\n', modified: 'a\nc\n' })
    await vi.waitFor(() => {
      const diffMsg = scope.posted.find(
        (m): m is { t: string; id: number; hunks: unknown[] } =>
          typeof m === 'object' &&
          m !== null &&
          't' in m &&
          m.t === 'diffResult' &&
          'id' in m &&
          m.id === 9,
      )
      expect(diffMsg).toBeDefined()
      expect(diffMsg!.hunks.length).toBeGreaterThan(0)
    })
  })
})
