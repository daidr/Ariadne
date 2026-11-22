import type { WorkspaceLevel } from './store'

export type WorkspaceLayoutMode = 'narrow' | 'medium' | 'wide'

export interface LayoutBreakpoints {
  md: boolean
  xl: boolean
}

export interface WorkspacePaneVisibility {
  projectsSidebarVisible: boolean
  coordinatorVisible: boolean
  sideVisible: boolean
  projectSplitSinglePane: 'start' | 'end' | undefined
  contentSplitSinglePane: 'start' | 'end' | undefined
}

/**
 * Three workspace tiers aligned with dou-ui's theme breakpoints:
 * - narrow (< 768px): phones, single pane drill-down
 * - medium (768px .. 1279px): tablets and foldables, projects in a header popover with coordinator and side pane side by side
 * - wide (>= 1280px): desktop, permanent three-column workspace
 */
export function resolveWorkspaceLayout(breakpoints: LayoutBreakpoints): WorkspaceLayoutMode {
  if (breakpoints.xl) return 'wide'
  if (breakpoints.md) return 'medium'
  return 'narrow'
}

export function resolveWorkspacePanes(options: {
  mode: WorkspaceLayoutMode
  workspaceLevel: WorkspaceLevel
  projectsCollapsed: boolean
  mediumSideCollapsed: boolean
}): WorkspacePaneVisibility {
  const { mode, workspaceLevel, projectsCollapsed, mediumSideCollapsed } = options
  if (mode === 'narrow') {
    const sideVisible = workspaceLevel === 'side'
    return {
      projectsSidebarVisible: workspaceLevel === 'projects',
      coordinatorVisible: workspaceLevel === 'coordinator',
      sideVisible,
      projectSplitSinglePane: workspaceLevel === 'projects' ? 'start' : 'end',
      contentSplitSinglePane: sideVisible ? 'end' : 'start',
    }
  }
  if (mode === 'medium') {
    return {
      projectsSidebarVisible: false,
      coordinatorVisible: true,
      sideVisible: !mediumSideCollapsed,
      projectSplitSinglePane: 'end',
      contentSplitSinglePane: mediumSideCollapsed ? 'start' : undefined,
    }
  }
  return {
    projectsSidebarVisible: !projectsCollapsed,
    coordinatorVisible: true,
    sideVisible: true,
    projectSplitSinglePane: undefined,
    contentSplitSinglePane: undefined,
  }
}
