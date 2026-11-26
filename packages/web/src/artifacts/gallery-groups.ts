import type { GalleryArtifactSummary } from '@ariadne/core'

export type GalleryGroupBy = 'none' | 'time' | 'project'
export type GalleryTimeBucket = 'today' | 'thisWeek' | 'earlier'

export interface GallerySection {
  key: string
  timeBucket?: GalleryTimeBucket
  projectId?: string
  projectName?: string
  items: GalleryArtifactSummary[]
}

const DAY_MS = 24 * 60 * 60 * 1000

/** Bucket a timestamp into today / past 7 days / earlier using the user's local calendar day. */
export function resolveGalleryTimeBucket(timestamp: number, now: number): GalleryTimeBucket {
  const current = new Date(now)
  const startOfToday = new Date(
    current.getFullYear(),
    current.getMonth(),
    current.getDate(),
  ).getTime()
  if (timestamp >= startOfToday) return 'today'
  if (timestamp >= startOfToday - 6 * DAY_MS) return 'thisWeek'
  return 'earlier'
}

/** Group gallery items by time or project while preserving item order within each section. */
export function groupGalleryItems(
  items: readonly GalleryArtifactSummary[],
  groupBy: GalleryGroupBy,
  now: number,
): GallerySection[] {
  if (items.length === 0) return []
  if (groupBy === 'none') {
    return [{ key: 'all', items: [...items] }]
  }
  if (groupBy === 'time') {
    const order: GalleryTimeBucket[] = ['today', 'thisWeek', 'earlier']
    const buckets: Record<GalleryTimeBucket, GalleryArtifactSummary[]> = {
      today: [],
      thisWeek: [],
      earlier: [],
    }
    for (const item of items) {
      const bucket = resolveGalleryTimeBucket(item.artifact.updatedAt, now)
      buckets[bucket].push(item)
    }
    return order
      .filter((bucket) => buckets[bucket].length > 0)
      .map((bucket) => ({
        key: `time:${bucket}`,
        timeBucket: bucket,
        items: buckets[bucket],
      }))
  }

  const byProject = new Map<string, GallerySection>()
  for (const item of items) {
    const projectId = item.artifact.projectId
    let section = byProject.get(projectId)
    if (!section) {
      section = {
        key: `project:${projectId}`,
        projectId,
        projectName: item.projectName,
        items: [],
      }
      byProject.set(projectId, section)
    }
    section.items.push(item)
  }
  return [...byProject.values()]
}

export function formatGalleryTime(timestamp: number, now: number, currentLocale: string): string {
  const date = new Date(timestamp)
  const current = new Date(now)
  const sameYear = date.getFullYear() === current.getFullYear()
  const sameDay =
    sameYear && date.getMonth() === current.getMonth() && date.getDate() === current.getDate()
  const options: Intl.DateTimeFormatOptions = sameDay
    ? { hour: '2-digit', minute: '2-digit' }
    : sameYear
      ? { month: 'short', day: 'numeric' }
      : { dateStyle: 'medium' }
  return date.toLocaleString(currentLocale, options)
}

export function formatGalleryUpdater(
  item: GalleryArtifactSummary,
  labels: { coordinator: string; user: string },
): string {
  if (item.updatedByLabel) return item.updatedByLabel
  if (item.updatedByCoordinator) return labels.coordinator
  if (item.artifact.updatedBy === 'user') return labels.user
  return item.artifact.updatedBy
}
