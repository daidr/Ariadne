import type {
  AnyStoredEvent,
  GalleryArtifactSummary,
  GalleryArtifactsQuery,
  GalleryCounts,
  GalleryKindFilter,
  GallerySort,
  GalleryStatusFilter,
} from '@ariadne/core'
import { computed, reactive } from 'vue'
import { api } from '../api'
import { onLiveEvent } from '../store'
import type { GalleryGroupBy } from './gallery-groups'

export type GalleryViewMode = 'grid' | 'list'
export type GalleryDensity = 'comfortable' | 'compact'

const PREFS_KEY = 'ariadne.gallery.prefs'
const GALLERY_PAGE_SIZE = 48

interface GalleryPrefs {
  viewMode: GalleryViewMode
  density: GalleryDensity
  groupBy: GalleryGroupBy
}

function readGalleryPrefs(): GalleryPrefs {
  const fallback: GalleryPrefs = {
    viewMode: 'grid',
    density: 'comfortable',
    groupBy: 'none',
  }
  try {
    const raw = localStorage.getItem(PREFS_KEY)
    if (!raw) return fallback
    const parsed = JSON.parse(raw) as Partial<GalleryPrefs>
    return {
      viewMode: parsed.viewMode === 'list' ? 'list' : 'grid',
      density: parsed.density === 'compact' ? 'compact' : 'comfortable',
      groupBy: parsed.groupBy === 'time' || parsed.groupBy === 'project' ? parsed.groupBy : 'none',
    }
  } catch {
    return fallback
  }
}

function writeGalleryPrefs(prefs: GalleryPrefs) {
  try {
    localStorage.setItem(PREFS_KEY, JSON.stringify(prefs))
  } catch {
    // Ignore storage errors in private browsing or full quota environments.
  }
}

const initialPrefs = readGalleryPrefs()

export const galleryState = reactive({
  projectId: '' as string,
  kindFilter: '' as GalleryKindFilter,
  status: '' as GalleryStatusFilter,
  query: '' as string,
  sort: 'updated_desc' as GallerySort,

  viewMode: initialPrefs.viewMode as GalleryViewMode,
  density: initialPrefs.density as GalleryDensity,
  groupBy: initialPrefs.groupBy as GalleryGroupBy,

  items: [] as GalleryArtifactSummary[],
  counts: null as GalleryCounts | null,
  nextPageToken: '' as string,
  hasMore: false,
  loading: false,
  loadingMore: false,
  error: null as string | null,
  loadedOnce: false,
  stale: false,
  staleRevision: 0,

  scrollTop: 0,
  selectedId: null as string | null,
  quickViewId: null as string | null,
})

export const hasActiveGalleryFilters = computed(() =>
  Boolean(
    galleryState.projectId ||
    galleryState.kindFilter ||
    galleryState.status ||
    galleryState.query.trim(),
  ),
)

export const activeGalleryFilterCount = computed(() => {
  let count = 0
  if (galleryState.projectId) count++
  if (galleryState.kindFilter) count++
  if (galleryState.status) count++
  if (galleryState.sort !== 'updated_desc') count++
  return count
})

export function saveGalleryPrefs() {
  writeGalleryPrefs({
    viewMode: galleryState.viewMode,
    density: galleryState.density,
    groupBy: galleryState.groupBy,
  })
}

let activeController: AbortController | null = null
let requestSerial = 0

function currentRpcParams(pageToken: string, includeCounts: boolean): GalleryArtifactsQuery {
  const kindFilter = galleryState.kindFilter
  const isProto = kindFilter === 'prototype'
  return {
    projectId: galleryState.projectId,
    kind: kindFilter === 'prototype' ? '' : kindFilter,
    onlyPrototypes: isProto,
    status: galleryState.status,
    query: galleryState.query.trim(),
    sort: galleryState.sort,
    pageSize: GALLERY_PAGE_SIZE,
    pageToken,
    includeCounts,
  }
}

export async function loadGallery(options: { silent?: boolean } = {}): Promise<void> {
  activeController?.abort()
  const controller = new AbortController()
  activeController = controller
  const serial = ++requestSerial
  const startRevision = galleryState.staleRevision

  if (!options.silent || !galleryState.loadedOnce) {
    galleryState.loading = true
  }
  galleryState.loadingMore = false
  galleryState.error = null

  try {
    const page = await api.listGalleryArtifacts({
      ...currentRpcParams('', true),
      signal: controller.signal,
    })
    if (serial !== requestSerial || controller.signal.aborted) return
    galleryState.items = page.items
    galleryState.nextPageToken = page.nextPageToken
    galleryState.hasMore = page.hasMore
    if (page.counts) galleryState.counts = page.counts
    galleryState.loadedOnce = true
    if (galleryState.staleRevision === startRevision) {
      galleryState.stale = false
    }
    if (
      galleryState.quickViewId &&
      !page.items.some((entry) => entry.artifact.id === galleryState.quickViewId)
    ) {
      galleryState.quickViewId = null
    }
  } catch (err) {
    if (serial !== requestSerial || controller.signal.aborted) return
    galleryState.error = err instanceof Error ? err.message : String(err)
  } finally {
    if (serial === requestSerial) {
      galleryState.loading = false
    }
  }
}

export async function loadMoreGallery(): Promise<void> {
  if (
    !galleryState.hasMore ||
    !galleryState.nextPageToken ||
    galleryState.loading ||
    galleryState.loadingMore
  ) {
    return
  }
  const controller = new AbortController()
  const serial = requestSerial
  galleryState.loadingMore = true
  try {
    const page = await api.listGalleryArtifacts({
      ...currentRpcParams(galleryState.nextPageToken, false),
      signal: controller.signal,
    })
    if (serial !== requestSerial || controller.signal.aborted) return
    const existing = new Set(galleryState.items.map((item) => item.artifact.id))
    const appended = page.items.filter((item) => !existing.has(item.artifact.id))
    galleryState.items = [...galleryState.items, ...appended]
    galleryState.nextPageToken = page.nextPageToken
    galleryState.hasMore = page.hasMore
  } catch (err) {
    if (serial !== requestSerial || controller.signal.aborted) return
    galleryState.error = err instanceof Error ? err.message : String(err)
  } finally {
    if (serial === requestSerial) {
      galleryState.loadingMore = false
    }
  }
}

export function resetGalleryFilters() {
  galleryState.projectId = ''
  galleryState.kindFilter = ''
  galleryState.status = ''
  galleryState.query = ''
  galleryState.sort = 'updated_desc'
  galleryState.scrollTop = 0
  void loadGallery()
}

/** Whether a live event can invalidate cached cross-project gallery data. */
export function isGalleryInvalidatingEvent(type: AnyStoredEvent['type']): boolean {
  return (
    type.startsWith('artifact.') ||
    type === 'project.deleted' ||
    type === 'project.renamed' ||
    type === 'plan.revised' ||
    type === 'thread.renamed'
  )
}

export function markGalleryStale() {
  galleryState.stale = true
  galleryState.staleRevision++
}

// Keep a module-level subscription so changes made while outside /gallery still invalidate cache.
onLiveEvent((event) => {
  if (!isGalleryInvalidatingEvent(event.type)) return
  if (event.type === 'project.deleted' && galleryState.projectId === event.projectId) {
    galleryState.projectId = ''
  }
  markGalleryStale()
})

export function resetGalleryStateForTests() {
  activeController?.abort()
  activeController = null
  requestSerial++
  galleryState.projectId = ''
  galleryState.kindFilter = ''
  galleryState.status = ''
  galleryState.query = ''
  galleryState.sort = 'updated_desc'
  galleryState.viewMode = 'grid'
  galleryState.density = 'comfortable'
  galleryState.groupBy = 'none'
  galleryState.items = []
  galleryState.counts = null
  galleryState.nextPageToken = ''
  galleryState.hasMore = false
  galleryState.loading = false
  galleryState.loadingMore = false
  galleryState.error = null
  galleryState.loadedOnce = false
  galleryState.stale = false
  galleryState.staleRevision = 0
  galleryState.scrollTop = 0
  galleryState.selectedId = null
  galleryState.quickViewId = null
}
