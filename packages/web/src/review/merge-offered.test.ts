import type { ThreadView } from '@ariadne/core'
import { describe, expect, test } from 'vitest'
import { branchChanges, mergeOffered } from './merge-offered'

const changes = { files: 1, added: 2, removed: 0 } as unknown as ThreadView['changes']
const thread = (workspace: Partial<NonNullable<ThreadView['workspace']>>) =>
  ({
    workspace: {
      label: '/w',
      branch: 'ariadne/x',
      base: 'main',
      removed: false,
      parked: false,
      ...workspace,
    },
    pullRequest: null,
    changes,
  }) as unknown as ThreadView

describe('what a removed worktree leaves to do', () => {
  test('one removed for good offers nothing', () => {
    expect(mergeOffered(thread({ removed: true }))).toBe(false)
    expect(branchChanges(thread({ removed: true }))).toBeNull()
  })

  test('one parked while idle still has its changes to look through and merge', () => {
    expect(mergeOffered(thread({ removed: true, parked: true }))).toBe(true)
    expect(branchChanges(thread({ removed: true, parked: true }))).toBe(changes)
  })
})
