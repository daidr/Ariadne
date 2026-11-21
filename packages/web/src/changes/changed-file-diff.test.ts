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
})
