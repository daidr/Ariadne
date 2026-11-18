// @vitest-environment happy-dom

import type { BranchChanges } from '@ariadne/plugin-sdk'
import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, test, vi } from 'vitest'
import { defineComponent, h } from 'vue'
import { api } from '../api'
import { useBranchUpdates } from './branch-updates'

const changes = (head: string, baseHead = 'base-tip'): BranchChanges => ({
  head,
  baseHead,
  branch: 'feature',
  base: 'main',
  from: 'merge-base',
  commits: [],
  files: [],
  uncommitted: [],
})

describe('branch comparison updates', () => {
  test('keeps the displayed comparison until the latest changes are opened', async () => {
    const get = vi
      .spyOn(api, 'getChanges')
      .mockResolvedValueOnce({ changes: changes('first'), mergeable: true })
      .mockResolvedValueOnce({ changes: changes('first', 'new-base-tip'), mergeable: true })
      .mockResolvedValueOnce({ changes: changes('second'), mergeable: true })
      .mockResolvedValueOnce({ changes: changes('third'), mergeable: true })
    let updates!: ReturnType<typeof useBranchUpdates>
    const view = mount(
      defineComponent({
        setup() {
          updates = useBranchUpdates(
            () => 'thread',
            () => true,
          )
          return () => h('div')
        },
      }),
    )
    try {
      await flushPromises()
      await updates.check(true)
      expect(updates.hasUpdate.value).toBe(false)
      expect(updates.changes.value?.baseHead).toBe('new-base-tip')

      await updates.check(true)
      expect(updates.hasUpdate.value).toBe(true)
      expect(updates.changes.value?.head).toBe('first')
      await updates.check(true)
      updates.showLatest()
      expect(updates.changes.value?.head).toBe('third')
      expect(updates.hasUpdate.value).toBe(false)
    } finally {
      view.unmount()
      get.mockRestore()
    }
  })
})
