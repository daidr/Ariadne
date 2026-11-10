import { DomainError, type Ariadne, type DashboardContent } from '@ariadne/core'
import type { DashboardSource } from '@ariadne/plugin-sdk'
import type { SharedArtifacts } from './shared-artifacts'

/** Live data is fetched by a plugin; past this it is too big to keep as a snapshot. */
const MAX_FETCHED_JSON = 2 * 1024 * 1024
/** A source that does not answer by then is given up on, so no request hangs on it. */
export const FETCH_TIMEOUT_MS = 30_000

/**
 * Check the settings the model chose against the source's schema: only known fields, of
 * their type, the required ones present. Defaults fill the rest. Settings go into the event
 * log as they are, so a field the plugin marks secret cannot be set here at all.
 */
export function checkSourceSettings(
  source: DashboardSource<never>,
  value: unknown,
): { ok: true; settings: Record<string, unknown> } | { ok: false; problem: string } {
  const input = value ?? {}
  if (typeof input !== 'object' || Array.isArray(input)) {
    return { ok: false, problem: '"source.settings" must be an object' }
  }
  const fields = source.settings.properties
  const settings: Record<string, unknown> = {}
  for (const [key, given] of Object.entries(input as Record<string, unknown>)) {
    const field = fields[key]
    if (!field) {
      const known = Object.keys(fields)
      return {
        ok: false,
        problem: `"source.settings.${key}" is not a setting of ${source.kind}${known.length ? `; it takes: ${known.join(', ')}` : ''}`,
      }
    }
    if (field.type === 'string' && field.secret) {
      return { ok: false, problem: `"source.settings.${key}" is a secret and cannot be set here` }
    }
    if (given === null || given === undefined) continue
    const type = field.type === 'enum' ? 'string' : field.type
    if (typeof given !== type || (type === 'number' && !Number.isFinite(given))) {
      return { ok: false, problem: `"source.settings.${key}" must be a ${type}` }
    }
    if (field.type === 'enum' && !field.options.includes(given as string)) {
      return {
        ok: false,
        problem: `"source.settings.${key}" must be one of: ${field.options.join(', ')}`,
      }
    }
    settings[key] = given
  }
  for (const [key, field] of Object.entries(fields)) {
    if (settings[key] === undefined && field.default !== undefined) settings[key] = field.default
  }
  for (const key of source.settings.required ?? []) {
    if (settings[key] === undefined || settings[key] === '') {
      return { ok: false, problem: `"source.settings.${key}" is required` }
    }
  }
  return { ok: true, settings }
}

/**
 * Ask the source for its data, within the timeout, and check what comes back: named data
 * sets in one object, small enough to keep. Throws with a reason the caller passes on.
 */
export async function fetchDashboardData(
  source: DashboardSource<never>,
  settings: Record<string, unknown>,
  context: { projectId: string; signal?: AbortSignal; timeoutMs?: number },
): Promise<Record<string, unknown>> {
  const timeout = AbortSignal.timeout(context.timeoutMs ?? FETCH_TIMEOUT_MS)
  const signal = context.signal ? AbortSignal.any([context.signal, timeout]) : timeout
  let fetched: unknown
  try {
    fetched = await new Promise((resolve, reject) => {
      // A source that ignores the signal still cannot hold the request past the timeout.
      const stop = () =>
        reject(new Error(timeout.aborted ? 'the source did not answer in time' : 'stopped'))
      if (signal.aborted) return stop()
      signal.addEventListener('abort', stop, { once: true })
      source.fetch(settings as never, { projectId: context.projectId, signal }).then(
        (value) => {
          signal.removeEventListener('abort', stop)
          resolve(value)
        },
        (error: unknown) => {
          signal.removeEventListener('abort', stop)
          reject(error)
        },
      )
    })
  } catch (error) {
    throw new Error(error instanceof Error ? error.message : String(error), { cause: error })
  }
  if (!fetched || typeof fetched !== 'object' || Array.isArray(fetched)) {
    throw new Error('the source returned no data sets')
  }
  if (JSON.stringify(fetched).length > MAX_FETCHED_JSON) throw new Error('the data is too large')
  return fetched as Record<string, unknown>
}

/**
 * Fetch a live dashboard's data again and store it as a new version: the data sets the
 * source returns replace those of the same name, the others stay, and it is marked as
 * taken now. A dashboard without a source has nothing to fetch.
 */
export async function refreshDashboard(
  app: Ariadne,
  sources: ReadonlyMap<string, DashboardSource<never>>,
  artifactId: string,
  input: {
    author: string
    reason: string
    signal?: AbortSignal
    now?: number
    /** Where it is edited together: the new data goes into its room, the layout there kept. */
    shared?: SharedArtifacts
  },
) {
  const current = app.getArtifact(artifactId)
  if (current.kind !== 'dashboard') {
    throw new DomainError('error.invalidBody', { reason: 'kind' })
  }
  const content = JSON.parse(current.content) as DashboardContent
  if (!content.source) throw new DomainError('error.dashboardStatic')
  const source = sources.get(content.source.kind)
  if (!source) throw new DomainError('error.dashboardSourceUnknown', { kind: content.source.kind })
  // The stored settings were checked when they were chosen; the plugin's schema may have
  // changed since, so they are checked again.
  const checked = checkSourceSettings(source, content.source.settings)
  if (!checked.ok) throw new DomainError('error.dashboardFetch', { detail: checked.problem })
  let fetched: Record<string, unknown>
  try {
    fetched = await fetchDashboardData(source, checked.settings, {
      projectId: current.projectId,
      ...(input.signal ? { signal: input.signal } : {}),
    })
  } catch (error) {
    throw new DomainError('error.dashboardFetch', { detail: (error as Error).message })
  }
  const takenAt = input.now ?? Date.now()
  if (input.shared) {
    // The layout as it is in the room now, which the user may have changed while it fetched.
    const now = JSON.parse(input.shared.content(artifactId)) as DashboardContent
    const next = { ...now, data: { ...now.data, ...fetched }, takenAt }
    return input.shared.write(artifactId, input.author, JSON.stringify(next), {
      reason: input.reason,
    })
  }
  const next: DashboardContent = { ...content, data: { ...content.data, ...fetched }, takenAt }
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
