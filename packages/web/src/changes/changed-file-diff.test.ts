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
  test('does not cap diff height and uses native scroll-y=false with sticky single-line toolbar', () => {
    expect(styleBlock).not.toMatch(/\bmax-(?:block-size|height)\s*:/)
    expect(styleBlock).not.toContain('.dou-code-editor__scroller')
    expect(sfcSource).toContain(':scroll-y="false"')
    expect(sfcSource).toContain('sticky-toolbar')
  })

  test('renders single-line sticky toolbar with file path and rename label inside DouCodeDiff', () => {
    const file: ChangedFile = {
      path: 'packages/web/src/changes/ChangedFileDiff.vue',
      previousPath: 'packages/web/src/changes/OldDiff.vue',
      status: 'renamed',
      added: 1,
      removed: 1,
    }
    const wrapper = mount(ChangedFileDiff, {
      props: {
        threadId: 'thr-diff-header',
        file,
        loadVersions: cachedLoader('const a = 1\n', 'const a = 2\n'),
        sent: [],
        notes: [],
        mode: 'split',
      },
      attachTo: document.body,
    })

    expect(wrapper.find('.file-diff-header').exists()).toBe(false)
    const diff = wrapper.find('.dou-code-diff')
    expect(diff.exists()).toBe(true)
    expect(diff.classes()).not.toContain('is-scroll-y')
    const toolbar = wrapper.find('.dou-code-diff__toolbar')
    expect(toolbar.exists()).toBe(true)
    expect(toolbar.classes()).toContain('is-sticky')
    expect(toolbar.find('.dou-code-diff__title').text()).toContain(file.path)
    expect(toolbar.find('.dou-code-diff__title').text()).toContain('OldDiff.vue')
    wrapper.unmount()
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

describe('ChangedFileDiff gutters', () => {
  test('omits the review-comments gutter column when the file has no review notes, and shows it reactively when notes arrive', async () => {
    const file: ChangedFile = {
      path: 'internal/runner/prompt.go',
      previousPath: null,
      status: 'modified',
      added: 1,
      removed: 1,
    }
    const wrapper = mount(ChangedFileDiff, {
      props: {
        threadId: 'thr-diff-gutter',
        file,
        loadVersions: cachedLoader('line 1\nline 2\n', 'line 1\nline 2 updated\n'),
        sent: [],
        notes: [],
        mode: 'inline',
      },
      attachTo: document.body,
    })

    await nextTick()
    const cellsWithoutNotes = wrapper.find('.dou-code-editor__number .dou-code-editor__cells')
    expect(cellsWithoutNotes.exists()).toBe(true)
    // Inline mode columns: line-comments + diff-original + diff-modified + diff sign (no empty review-comments column).
    expect(cellsWithoutNotes.findAll('.dou-code-editor__cell')).toHaveLength(4)
    expect(wrapper.find('.review-comment-mark').exists()).toBe(false)

    await wrapper.setProps({
      notes: [
        {
          id: 'r0c0',
          round: {
            threadId: 'thr-diff-gutter',
            round: 1,
            reviewerId: 'rev-1',
            head: 'abc123',
            requestedAt: 1,
            verdict: 'changes',
            abandoned: false,
            summary: 'Check line 2',
            submittedAt: 1,
            comments: [
              {
                path: file.path,
                line: 2,
                quote: 'line 2 updated',
                body: 'Please verify this change',
                kind: 'bug',
              },
            ],
            replies: [],
          },
          comment: {
            path: file.path,
            line: 2,
            quote: 'line 2 updated',
            body: 'Please verify this change',
            kind: 'bug',
          },
          reply: null,
        },
      ],
    })
    await nextTick()

    const cellsWithNotes = wrapper.find('.dou-code-editor__number .dou-code-editor__cells')
    expect(cellsWithNotes.findAll('.dou-code-editor__cell')).toHaveLength(5)
    expect(wrapper.find('.review-comment-mark').exists()).toBe(true)
    wrapper.unmount()
  })
})
