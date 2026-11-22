// @vitest-environment happy-dom
import type { ChangedFile, FileVersions } from '@ariadne/plugin-sdk'
import { flushPromises, mount } from '@vue/test-utils'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, test } from 'vitest'
import { nextTick } from 'vue'
import ChangedFileDiff from './ChangedFileDiff.vue'

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

describe('ChangedFileDiff single-line sticky header', () => {
  test('declares sticky positioning with opaque surface, bottom border, and stacking above diff spans', () => {
    expect(styleBlock).toMatch(
      /\.file-diff-header\s*\{[^}]*position:\s*sticky;[^}]*top:\s*0;[^}]*z-index:\s*5;/s,
    )
    expect(styleBlock).toMatch(
      /\.file-diff-header\s*\{[^}]*background-color:\s*var\(--dou-color-bg-canvas\);[^}]*border-bottom:\s*1px solid var\(--dou-color-border\);/s,
    )
    expect(styleBlock).not.toMatch(/\.file-diff\s*\{[^}]*overflow:\s*hidden/s)
  })

  test('merges path, diff stats, and toolbar controls into one header without rendering DouCodeDiff toolbar', async () => {
    const before = Array.from({ length: 80 }, (_, i) => `const v${i + 1} = ${i + 1}`).join('\n')
    const after = before.replace('const v40 = 40', 'const v40 = 400')
    const file: ChangedFile = {
      path: 'packages/web/src/changes/ChangesView.vue',
      previousPath: 'packages/web/src/changes/OldChangesView.vue',
      status: 'renamed',
      added: 1,
      removed: 1,
    }
    const loadVersions = Object.assign(
      async (): Promise<FileVersions> => ({ before, after, skipped: null }),
      {
        peek: (): FileVersions => ({ before, after, skipped: null }),
      },
    )

    const wrapper = mount(ChangedFileDiff, {
      props: {
        threadId: 'thr-diff-header',
        file,
        loadVersions,
        sent: [],
        notes: [],
        mode: 'split',
      },
      attachTo: document.body,
    })

    await nextTick()

    expect(wrapper.find('.dou-code-diff__toolbar').exists()).toBe(false)
    const header = wrapper.find('.file-diff-header')
    expect(header.exists()).toBe(true)
    expect(header.find('.file-diff-path').text()).toBe(file.path)
    expect(header.find('.file-diff-renamed').text()).toContain(file.previousPath!)
    expect(header.find('.file-diff-count.is-added').text()).toBe('+1')
    expect(header.find('.file-diff-count.is-removed').text()).toBe('−1')

    const expandButton = header.find('button[aria-label="展开全部"]')
    expect(expandButton.exists()).toBe(true)
    expect(wrapper.findAll('.dou-code-diff__bar').length).toBeGreaterThan(0)

    await expandButton.trigger('click')
    await nextTick()
    expect(wrapper.findAll('.dou-code-diff__bar')).toHaveLength(0)

    const collapseButton = header.find('button[aria-label="折叠未改动的行"]')
    expect(collapseButton.exists()).toBe(true)
    await collapseButton.trigger('click')
    await nextTick()
    expect(wrapper.findAll('.dou-code-diff__bar').length).toBeGreaterThan(0)

    const modeButton = header.find('button[aria-label="行内显示"]')
    expect(modeButton.exists()).toBe(true)
    await modeButton.trigger('click')
    await nextTick()
    expect(wrapper.emitted('update:mode')).toEqual([['inline']])

    wrapper.unmount()
  })

  test('shows git numstat and hides no-op collapse button on all-added one-sided file', async () => {
    const after = Array.from({ length: 20 }, (_, i) => `export const n${i} = ${i}`).join('\n')
    const file: ChangedFile = {
      path: 'packages/web/src/components/thread-tools.test.ts',
      previousPath: null,
      status: 'added',
      added: 20,
      removed: 0,
    }
    const loadVersions = Object.assign(
      async (): Promise<FileVersions> => ({ before: null, after, skipped: null }),
      {
        peek: (): FileVersions => ({ before: null, after, skipped: null }),
      },
    )

    const wrapper = mount(ChangedFileDiff, {
      props: {
        threadId: 'thr-diff-added',
        file,
        loadVersions,
        sent: [],
        notes: [],
        mode: 'auto',
      },
      attachTo: document.body,
    })

    await nextTick()

    const header = wrapper.find('.file-diff-header')
    expect(header.find('.file-diff-count.is-added').text()).toBe('+20')
    expect(header.find('.file-diff-count.is-removed').text()).toBe('−0')
    expect(header.find('button[aria-label="展开全部"]').exists()).toBe(false)
    expect(header.find('button[aria-label="折叠未改动的行"]').exists()).toBe(false)
    expect(wrapper.find('.dou-code-diff').classes()).toContain('dou-code-diff--inline')

    const splitButton = header.find('button[aria-label="并排显示"]')
    expect(splitButton.exists()).toBe(true)
    await splitButton.trigger('click')
    await nextTick()
    expect(wrapper.find('.dou-code-diff').classes()).toContain('dou-code-diff--split')

    wrapper.unmount()
  })
})
