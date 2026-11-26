import type { Component } from 'vue'
import {
  createRouter,
  type RouteLocationNormalized,
  type RouteLocationAsRelative,
  type RouterHistory,
} from 'vue-router'
import { isPaneKey, type PaneKey, type SideLayout } from './side/layout'
import type { ArtifactHistoryStack } from './artifacts/artifact-history'
import type { FullView, MainView, TurnChangesRef, WorkspaceLevel } from './store'

export interface WorkspaceLocation {
  projectId: string | null
  level: WorkspaceLevel
  main: MainView
  starting: boolean
  tab: PaneKey | null
  modal: PaneKey | null
  from: string | null
}

/** Per history entry, so another browser tab cannot overwrite the state of this one. */
export interface WorkspaceSnapshot {
  side: SideLayout
  paneParams: Record<string, { turn?: TurnChangesRef; path?: string }>
  full: FullView | null
  planWide: boolean
  mainTrail: { view: MainView; level: WorkspaceLevel }[]
  artifactHistory?: {
    full?: ArtifactHistoryStack
    side?: Record<string, ArtifactHistoryStack>
  }
}

export function createWorkspaceRouter(history: RouterHistory, workspace: Component) {
  return createRouter({
    history,
    routes: [
      { path: '/gallery', name: 'gallery', component: workspace },
      { path: '/projects/:projectId?', name: 'workspace', component: workspace },
      { path: '/:pathMatch(.*)*', redirect: { name: 'workspace' } },
    ],
  })
}

export function workspaceLocation(route: RouteLocationNormalized): WorkspaceLocation {
  const { query, params } = route
  if (route.name === 'gallery' || route.path === '/gallery') {
    return {
      projectId: null,
      level: query.pane === 'projects' ? 'projects' : 'coordinator',
      main: { kind: 'gallery' },
      starting: false,
      tab: null,
      modal: null,
      from: null,
    }
  }
  const projectId =
    typeof params.projectId === 'string' && params.projectId ? params.projectId : null
  const starting = query.new === '1'
  const level = starting
    ? 'coordinator'
    : !projectId || query.pane === 'projects'
      ? 'projects'
      : query.pane === 'side'
        ? 'side'
        : 'coordinator'
  let main: MainView = { kind: 'conversation' }
  if (query.view === 'plan' || query.view === 'artifacts') main = { kind: query.view }
  if (query.view === 'trace' && typeof query.trace === 'string' && query.trace) {
    main = { kind: 'trace', conversationId: query.trace }
  }
  return {
    projectId,
    level,
    main,
    starting,
    tab: isPaneKey(query.tab) ? query.tab : null,
    modal: isPaneKey(query.modal) ? query.modal : null,
    from: typeof query.from === 'string' && query.from ? query.from : null,
  }
}

export function workspaceAddress(location: WorkspaceLocation): RouteLocationAsRelative {
  const { projectId, level, main, starting, tab, modal, from } = location
  if (main.kind === 'gallery') {
    return {
      name: 'gallery',
      query: {
        ...(level === 'projects' && { pane: 'projects' }),
      },
    }
  }
  return {
    name: 'workspace',
    params: { projectId: projectId ?? '' },
    query: {
      ...(projectId && level !== 'coordinator' && { pane: level }),
      ...(starting && { new: '1' }),
      ...(main.kind !== 'conversation' && { view: main.kind }),
      ...(main.kind === 'trace' && { trace: main.conversationId }),
      ...(projectId && tab && { tab }),
      ...(modal && { modal }),
      ...(modal && from && { from }),
    },
  }
}
