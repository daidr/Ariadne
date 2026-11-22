// @vitest-environment happy-dom
import type { ChangedFile, FileVersions } from '@ariadne/plugin-sdk'
import { shikiLanguage } from '@dou-ui/code/shiki'
import { flushPromises, mount } from '@vue/test-utils'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { createJavaScriptRegexEngine } from 'shiki/engine/javascript'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { nextTick } from 'vue'
import { resetCodeLanguageCache } from '../highlighting/code-language'
import { createShikiService } from '../highlighting/shiki'
import ChangedFileDiff from './ChangedFileDiff.vue'

const testShiki = createShikiService(() => createJavaScriptRegexEngine())
const loadShikiMock = vi.fn(async (language: string) => {
  const loaded = await testShiki.loadLanguage(language)
  return loaded ? shikiLanguage(loaded.engine, loaded.name) : null
})

vi.mock('../highlighting/shiki-code-language', () => ({
  loadShikiCodeLanguage: (language: string) => loadShikiMock(language),
}))

const sfcSource = readFileSync(resolve(import.meta.dirname, 'ChangedFileDiff.vue'), 'utf8')
const styleBlock = sfcSource.match(/<style[^>]*>([\s\S]*?)<\/style>/)?.[1] ?? ''

describe('ChangedFileDiff vertical scrolling', () => {
  test('does not cap diff height and disables inner editor vertical scrollbar while keeping horizontal scroll', () => {
    expect(styleBlock).not.toMatch(/\bmax-(?:block-size|height)\s*:/)
    expect(styleBlock).toMatch(
      /\.diff\s+:deep\(\.dou-code-editor__scroller\)\s*\{[^}]*overflow-x:\s*auto;[^}]*overflow-y:\s*hidden;/s,
    )
  })

  test('expands unchanged sections and preserves editor scrollers without height cap', async () => {
    const before = Array.from({ length: 120 }, (_, i) => `line ${i + 1}`).join('\n')
    const after = before.replace('line 60', 'line 60 changed')
    const file: ChangedFile = {
      path: 'sample.ts',
      previousPath: null,
      status: 'modified',
      added: 1,
      removed: 1,
    }
    const loadVersions = async (): Promise<FileVersions> => ({ before, after, skipped: null })

    const wrapper = mount(ChangedFileDiff, {
      props: {
        threadId: 'thr-diff-scroll',
        file,
        loadVersions,
        sent: [],
        notes: [],
        mode: 'split',
      },
      attachTo: document.body,
    })

    await flushPromises()
    await nextTick()
    await new Promise((done) => setTimeout(done, 50))

    const diff = wrapper.find('.dou-code-diff')
    expect(diff.exists()).toBe(true)
    expect(wrapper.findAll('.dou-code-editor__scroller').length).toBeGreaterThan(0)

    const barsBefore = wrapper.findAll('.dou-code-diff__bar')
    expect(barsBefore.length).toBeGreaterThan(0)
    await barsBefore[0]!.trigger('click')
    await nextTick()
    expect(wrapper.findAll('.dou-code-diff__bar').length).toBeLessThan(barsBefore.length)

    wrapper.unmount()
  })

  test('renders synchronously from peek cache without flashing loading spinner', () => {
    const file: ChangedFile = {
      path: 'cached.ts',
      previousPath: null,
      status: 'modified',
      added: 1,
      removed: 0,
    }
    let asyncCalls = 0
    const loadVersions = Object.assign(
      async (): Promise<FileVersions> => {
        asyncCalls++
        return { before: 'a\n', after: 'a\nb\n', skipped: null }
      },
      {
        peek: (): FileVersions => ({ before: 'a\n', after: 'a\nb\n', skipped: null }),
      },
    )

    const wrapper = mount(ChangedFileDiff, {
      props: {
        threadId: 'thr-diff-cached',
        file,
        loadVersions,
        sent: [],
        notes: [],
        mode: 'split',
      },
      attachTo: document.body,
    })

    expect(wrapper.find('.file-diff-loading').exists()).toBe(false)
    expect(wrapper.find('.dou-code-diff').exists()).toBe(true)
    expect(asyncCalls).toBe(0)
    wrapper.unmount()
  })
})

function cachedLoader(before: string, after: string) {
  return Object.assign(async (): Promise<FileVersions> => ({ before, after, skipped: null }), {
    peek: (): FileVersions => ({ before, after, skipped: null }),
  })
}

describe('ChangedFileDiff syntax highlighting', () => {
  afterEach(() => {
    resetCodeLanguageCache()
    loadShikiMock.mockClear()
  })

  test('highlights built-in languages immediately in split and inline modes', () => {
    const file: ChangedFile = {
      path: 'packages/web/src/demo.ts',
      previousPath: null,
      status: 'modified',
      added: 1,
      removed: 1,
    }
    const loadVersions = cachedLoader(
      'export const count: number = 1\n',
      'export const count: number = 2\n',
    )

    for (const mode of ['split', 'inline'] as const) {
      const wrapper = mount(ChangedFileDiff, {
        props: { threadId: 'thr-diff-ts', file, loadVersions, sent: [], notes: [], mode },
        attachTo: document.body,
      })

      expect(wrapper.findAll('.dou-syntax-keyword').length).toBeGreaterThan(0)
      expect(wrapper.findAll('.dou-syntax-number').length).toBeGreaterThan(0)
      expect(loadShikiMock).not.toHaveBeenCalled()
      wrapper.unmount()
    }
  })

  test('highlights Go and Protobuf diffs via Shiki fallback and caches the resolved language', async () => {
    const goFile: ChangedFile = {
      path: 'internal/core/server.go',
      previousPath: null,
      status: 'modified',
      added: 1,
      removed: 1,
    }
    const goLoader = cachedLoader(
      'package core\nfunc Start(port int) error {\n\treturn nil\n}\n',
      'package core\nfunc Start(port int) error {\n\tprintln("ready")\n\treturn nil\n}\n',
    )

    const first = mount(ChangedFileDiff, {
      props: {
        threadId: 'thr-diff-go',
        file: goFile,
        loadVersions: goLoader,
        sent: [],
        notes: [],
        mode: 'split',
      },
      attachTo: document.body,
    })

    await vi.waitFor(() => {
      expect(first.findAll('.dou-syntax-keyword').length).toBeGreaterThan(0)
    })
    expect(first.findAll('.dou-syntax-function').length).toBeGreaterThan(0)
    expect(first.findAll('.dou-syntax-string').length).toBeGreaterThan(0)
    expect(loadShikiMock).toHaveBeenCalledTimes(1)
    first.unmount()

    // Second Go file hits the cached Shiki tokenizer synchronously without another load.
    const second = mount(ChangedFileDiff, {
      props: {
        threadId: 'thr-diff-go-2',
        file: { ...goFile, path: 'cmd/ariadne/main.go' },
        loadVersions: goLoader,
        sent: [],
        notes: [],
        mode: 'inline',
      },
      attachTo: document.body,
    })

    expect(second.findAll('.dou-syntax-keyword').length).toBeGreaterThan(0)
    expect(loadShikiMock).toHaveBeenCalledTimes(1)
    second.unmount()

    const protoFile: ChangedFile = {
      path: 'proto/ariadne/v1/file.proto',
      previousPath: null,
      status: 'added',
      added: 3,
      removed: 0,
    }
    const proto = mount(ChangedFileDiff, {
      props: {
        threadId: 'thr-diff-proto',
        file: protoFile,
        loadVersions: cachedLoader('', 'syntax = "proto3";\nmessage Ping {\n  string id = 1;\n}\n'),
        sent: [],
        notes: [],
        mode: 'split',
      },
      attachTo: document.body,
    })

    await vi.waitFor(() => {
      expect(proto.findAll('.dou-syntax-keyword').length).toBeGreaterThan(0)
    })
    expect(proto.findAll('.dou-syntax-string').length).toBeGreaterThan(0)
    proto.unmount()
  })
})
