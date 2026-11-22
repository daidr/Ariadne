import { describe, expect, test } from 'vitest'
import { resolveWorkspaceLayout, resolveWorkspacePanes } from './workspace-layout'

describe('resolveWorkspaceLayout', () => {
  test('maps breakpoints into narrow, medium and wide tiers', () => {
    expect(resolveWorkspaceLayout({ md: false, xl: false })).toBe('narrow')
    expect(resolveWorkspaceLayout({ md: true, xl: false })).toBe('medium')
    expect(resolveWorkspaceLayout({ md: true, xl: true })).toBe('wide')
  })
})

describe('resolveWorkspacePanes', () => {
  test('shows one pane at a time in narrow mode', () => {
    expect(
      resolveWorkspacePanes({
        mode: 'narrow',
        workspaceLevel: 'projects',
        projectsCollapsed: false,
        mediumSideCollapsed: false,
      }),
    ).toEqual({
      projectsSidebarVisible: true,
      coordinatorVisible: false,
      sideVisible: false,
      projectSplitSinglePane: 'start',
      contentSplitSinglePane: 'start',
    })

    expect(
      resolveWorkspacePanes({
        mode: 'narrow',
        workspaceLevel: 'coordinator',
        projectsCollapsed: false,
        mediumSideCollapsed: false,
      }),
    ).toEqual({
      projectsSidebarVisible: false,
      coordinatorVisible: true,
      sideVisible: false,
      projectSplitSinglePane: 'end',
      contentSplitSinglePane: 'start',
    })

    expect(
      resolveWorkspacePanes({
        mode: 'narrow',
        workspaceLevel: 'side',
        projectsCollapsed: false,
        mediumSideCollapsed: false,
      }),
    ).toEqual({
      projectsSidebarVisible: false,
      coordinatorVisible: false,
      sideVisible: true,
      projectSplitSinglePane: 'end',
      contentSplitSinglePane: 'end',
    })
  })

  test('hides the permanent project sidebar and pairs coordinator with side pane in medium mode', () => {
    expect(
      resolveWorkspacePanes({
        mode: 'medium',
        workspaceLevel: 'projects',
        projectsCollapsed: false,
        mediumSideCollapsed: false,
      }),
    ).toEqual({
      projectsSidebarVisible: false,
      coordinatorVisible: true,
      sideVisible: true,
      projectSplitSinglePane: 'end',
      contentSplitSinglePane: undefined,
    })

    expect(
      resolveWorkspacePanes({
        mode: 'medium',
        workspaceLevel: 'coordinator',
        projectsCollapsed: false,
        mediumSideCollapsed: true,
      }),
    ).toEqual({
      projectsSidebarVisible: false,
      coordinatorVisible: true,
      sideVisible: false,
      projectSplitSinglePane: 'end',
      contentSplitSinglePane: 'start',
    })
  })

  test('keeps coordinator and side pane visible in wide mode regardless of medium collapse state', () => {
    expect(
      resolveWorkspacePanes({
        mode: 'wide',
        workspaceLevel: 'projects',
        projectsCollapsed: false,
        mediumSideCollapsed: true,
      }),
    ).toEqual({
      projectsSidebarVisible: true,
      coordinatorVisible: true,
      sideVisible: true,
      projectSplitSinglePane: undefined,
      contentSplitSinglePane: undefined,
    })

    expect(
      resolveWorkspacePanes({
        mode: 'wide',
        workspaceLevel: 'coordinator',
        projectsCollapsed: true,
        mediumSideCollapsed: true,
      }),
    ).toEqual({
      projectsSidebarVisible: false,
      coordinatorVisible: true,
      sideVisible: true,
      projectSplitSinglePane: undefined,
      contentSplitSinglePane: undefined,
    })
  })
})
