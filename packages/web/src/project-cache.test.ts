import { afterEach, expect, test, vi } from 'vitest'
import { api } from './api'
import { clearBoardThumbnails } from './artifacts/board-thumbnail'
import { clearLinkCaches } from './markdown/link-summaries'
import { deleteProject, openProject, state } from './store'

vi.mock('./artifacts/board-thumbnail', () => ({ clearBoardThumbnails: vi.fn() }))
vi.mock('./markdown/link-summaries', () => ({ clearLinkCaches: vi.fn() }))
afterEach(() => {
  vi.restoreAllMocks()
  vi.clearAllMocks()
})

test('switching projects clears visual caches before any new project data arrives', async () => {
  vi.spyOn(api, 'getProject').mockRejectedValue(new Error('fixture'))
  await openProject('next')
  expect(clearBoardThumbnails).toHaveBeenCalledTimes(1)
  expect(clearLinkCaches).toHaveBeenCalledTimes(1)
})

test('deleting even an inactive project releases cached results', async () => {
  state.currentId = null
  vi.spyOn(api, 'deleteProject').mockResolvedValue(undefined)
  vi.spyOn(api, 'listProjects').mockResolvedValue({ projects: [], lastSeq: 0 })
  await deleteProject('gone')
  expect(clearBoardThumbnails).toHaveBeenCalledTimes(1)
  expect(clearLinkCaches).toHaveBeenCalledTimes(1)
})
