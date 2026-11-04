import { DomainError, type Ariadne, type DashboardContent } from '@ariadne/core'
import type { DashboardSource } from '@ariadne/plugin-sdk'

/** Live data is fetched by a plugin; past this it is too big to keep as a snapshot. */
const MAX_FETCHED_JSON = 2 * 1024 * 1024

/**
 * Fetch a live dashboard's data again and store it as a new version: the data sets the
 * source returns replace those of the same name, the others stay, and it is marked as
 * taken now. A dashboard without a source has nothing to fetch.
 */
export async function refreshDashboard(
  app: Ariadne,
  sources: ReadonlyMap<string, DashboardSource<never>>,
  artifactId: string,
  input: { author: string; reason: string; signal?: AbortSignal; now?: number },
) {
  const current = app.getArtifact(artifactId)
  if (current.kind !== 'dashboard') {
    throw new DomainError('error.invalidBody', { reason: 'kind' })
  }
  const content = JSON.parse(current.content) as DashboardContent
  if (!content.source) throw new DomainError('error.dashboardStatic')
  const source = sources.get(content.source.kind)
  if (!source) throw new DomainError('error.dashboardSourceUnknown', { kind: content.source.kind })
  let fetched: Record<string, unknown>
  try {
    fetched = await source.fetch((content.source.settings ?? {}) as never, {
      projectId: current.projectId,
      ...(input.signal ? { signal: input.signal } : {}),
    })
  } catch (error) {
    throw new DomainError('error.dashboardFetch', {
      detail: error instanceof Error ? error.message : String(error),
    })
  }
  if (!fetched || typeof fetched !== 'object' || Array.isArray(fetched)) {
    throw new DomainError('error.dashboardFetch', { detail: 'the source returned no data sets' })
  }
  if (JSON.stringify(fetched).length > MAX_FETCHED_JSON) {
    throw new DomainError('error.dashboardFetch', { detail: 'the data is too large' })
  }
  const next: DashboardContent = {
    ...content,
    data: { ...content.data, ...fetched },
    takenAt: input.now ?? Date.now(),
  }
  return app.reviseArtifact(artifactId, {
    content: JSON.stringify(next),
    author: input.author,
    reason: input.reason,
    baseVersion: current.version,
  })
}

/** What the model reads about the sources it may name. */
export function describeSources(sources: ReadonlyMap<string, DashboardSource<never>>) {
  return [...sources.values()].map((source) => ({
    kind: source.kind,
    description: source.description,
    settings: source.settings,
  }))
}
