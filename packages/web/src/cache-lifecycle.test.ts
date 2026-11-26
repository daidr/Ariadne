import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { api } from './api'
import {
  boardThumbnail,
  clearBoardThumbnails,
  THUMBNAIL_CONCURRENCY,
  THUMBNAIL_LIMIT,
  THUMBNAIL_TTL_MS,
  MAX_THUMBNAIL_CHARS,
} from './artifacts/board-thumbnail'
import {
  clearLinkCaches,
  linkSummary,
  linkPreview,
  SUMMARY_LIMIT,
  SUMMARY_TTL_MS,
  PREVIEW_LIMIT,
  PREVIEW_TTL_MS,
} from './markdown/link-summaries'

vi.mock('./api', () => ({
  api: { getArtifact: vi.fn(), summarizeLinks: vi.fn(), previewLink: vi.fn() },
}))
vi.mock('@dou-ui/flow', () => ({ renderFlowSVG: vi.fn(async () => '<svg />') }))
vi.mock('@dou-ui/flow/style.css', () => ({}))
vi.mock('./artifacts/whiteboard-extensions', () => ({ whiteboardExtensions: () => [] }))

beforeEach(() => {
  vi.useFakeTimers()
  vi.mocked(api.getArtifact)
    .mockReset()
    .mockImplementation(async () => ({ artifact: { content: '{"nodes":[{}]}' } }) as never)
  vi.mocked(api.summarizeLinks)
    .mockReset()
    .mockImplementation(async (urls) => ({
      links: Object.fromEntries(urls.map((url) => [url, null])),
    }))
  vi.mocked(api.previewLink).mockReset().mockResolvedValue({ preview: null })
})
afterEach(async () => {
  clearBoardThumbnails()
  clearLinkCaches()
  await vi.waitFor(() => expect(vi.getTimerCount()).toBe(0))
  vi.useRealTimers()
})

test('thumbnail reuse, LRU capacity, idle expiry, revision and theme invalidation', async () => {
  expect(await boardThumbnail('oldest', 1, 'light')).toContain('data:image/svg+xml')
  await boardThumbnail('oldest', 1, 'light')
  expect(api.getArtifact).toHaveBeenCalledTimes(1)
  for (let i = 0; i < THUMBNAIL_LIMIT; i++) await boardThumbnail(String(i), 1, 'light')
  await boardThumbnail('oldest', 1, 'light')
  expect(api.getArtifact).toHaveBeenCalledTimes(THUMBNAIL_LIMIT + 2)
  await boardThumbnail('oldest', 2, 'light')
  await boardThumbnail('oldest', 2, 'dark')
  const calls = vi.mocked(api.getArtifact).mock.calls.length
  await vi.advanceTimersByTimeAsync(THUMBNAIL_TTL_MS)
  expect(vi.getTimerCount()).toBe(0)
  await boardThumbnail('oldest', 2, 'dark')
  expect(api.getArtifact).toHaveBeenCalledTimes(calls + 1)
})

test('clearing thumbnails aborts in-flight work, drops queued jobs, and rejects late results', async () => {
  let finish!: (value: Awaited<ReturnType<typeof api.getArtifact>>) => void
  vi.mocked(api.getArtifact).mockImplementationOnce(
    () =>
      new Promise((resolve) => {
        finish = resolve
      }),
  )
  const first = boardThumbnail('first', 1, 'light')
  const queued = boardThumbnail('queued', 1, 'light')
  clearBoardThumbnails()
  expect(await first).toBeNull()
  expect(await queued).toBeNull()
  expect(vi.mocked(api.getArtifact).mock.calls[0]?.[1]?.signal?.aborted).toBe(true)
  finish({ artifact: { content: '{"nodes":[{}]}' } } as never)
  const again = boardThumbnail('first', 1, 'light')
  expect(await again).toContain('data:image/svg+xml')
  expect(api.getArtifact).toHaveBeenCalledTimes(2)
})

test('a burst has bounded queued render work and oversized SVGs are not retained', async () => {
  const jobs = Array.from({ length: THUMBNAIL_LIMIT * 3 }, (_, i) =>
    boardThumbnail(String(i), 1, 'light'),
  )
  await Promise.all(jobs)
  expect(vi.mocked(api.getArtifact).mock.calls.length).toBeLessThanOrEqual(THUMBNAIL_LIMIT + 1)
  const { renderFlowSVG } = await import('@dou-ui/flow')
  vi.mocked(renderFlowSVG).mockResolvedValueOnce('x'.repeat(MAX_THUMBNAIL_CHARS + 1))
  expect(await boardThumbnail('too-large', 1, 'light')).toBeNull()
})

test('thumbnails fetch lightweight whiteboard payloads with bounded concurrency', async () => {
  const resolvers: Array<() => void> = []
  let inFlight = 0
  let maxInFlight = 0
  vi.mocked(api.getArtifact).mockImplementation(
    () =>
      new Promise((resolve) => {
        inFlight++
        maxInFlight = Math.max(maxInFlight, inFlight)
        resolvers.push(() => {
          inFlight--
          resolve({ artifact: { content: '{"nodes":[{}]}' } } as never)
        })
      }),
  )
  const jobs = Array.from({ length: THUMBNAIL_CONCURRENCY + 2 }, (_, i) =>
    boardThumbnail(`concurrent-${i}`, 1, 'light'),
  )
  await Promise.resolve()
  expect(maxInFlight).toBe(THUMBNAIL_CONCURRENCY)
  expect(vi.mocked(api.getArtifact).mock.calls[0]?.[1]).toMatchObject({
    versions: false,
    lightweight: true,
  })
  for (let done = 0; done < jobs.length; done++) {
    await vi.waitFor(() => expect(resolvers.length).toBeGreaterThan(0))
    resolvers.shift()!()
  }
  const results = await Promise.all(jobs)
  expect(results.every((url) => url?.includes('data:image/svg+xml'))).toBe(true)
})

test('summaries deduplicate and batch, then release all results (including misses) on expiry or clear', async () => {
  const first = linkSummary('https://a.invalid')
  expect(linkSummary('https://a.invalid')).toBe(first)
  await Promise.all([first, linkSummary('https://b.invalid')])
  expect(api.summarizeLinks).toHaveBeenCalledTimes(1)
  expect(vi.mocked(api.summarizeLinks).mock.calls[0]![0]).toHaveLength(2)
  await vi.advanceTimersByTimeAsync(SUMMARY_TTL_MS)
  expect(vi.getTimerCount()).toBe(0)
  await linkSummary('https://a.invalid')
  expect(api.summarizeLinks).toHaveBeenCalledTimes(2)
  clearLinkCaches()
  await linkSummary('https://a.invalid')
  expect(api.summarizeLinks).toHaveBeenCalledTimes(3)
})

test('a link burst stays bounded, and newly requested work after a batch settles is not stranded', async () => {
  const old = linkSummary('https://old.invalid')
  const jobs = Array.from({ length: SUMMARY_LIMIT * 3 }, (_, i) =>
    linkSummary(`https://a.invalid/${i}`),
  )
  await Promise.all([old, ...jobs])
  const fetched = vi.mocked(api.summarizeLinks).mock.calls.flatMap(([urls]) => urls)
  expect(fetched.length).toBeLessThanOrEqual(SUMMARY_LIMIT)
  expect(fetched).not.toContain('https://old.invalid')
  await linkSummary('https://new.invalid')
  expect(vi.mocked(api.summarizeLinks).mock.calls.at(-1)![0]).toContain('https://new.invalid')
})

test('preview count is bounded and idle expiration aborts pending requests', async () => {
  await linkPreview('https://old.invalid')
  for (let i = 0; i < PREVIEW_LIMIT; i++) await linkPreview(`https://a.invalid/${i}`)
  await linkPreview('https://old.invalid')
  expect(api.previewLink).toHaveBeenCalledTimes(PREVIEW_LIMIT + 2)
  const signals = vi.mocked(api.previewLink).mock.calls.map(([, signal]) => signal!)
  await vi.advanceTimersByTimeAsync(PREVIEW_TTL_MS)
  expect(signals.every((signal) => signal.aborted)).toBe(true)
  expect(vi.getTimerCount()).toBe(0)
})

test('clearing link caches aborts requests and late responses never refill them', async () => {
  let finish!: (value: Awaited<ReturnType<typeof api.previewLink>>) => void
  vi.mocked(api.previewLink).mockImplementationOnce(
    () =>
      new Promise((resolve) => {
        finish = resolve
      }),
  )
  const old = linkPreview('https://late.invalid')
  clearLinkCaches()
  expect(await old).toBeNull()
  expect(vi.mocked(api.previewLink).mock.calls[0]![1]!.aborted).toBe(true)
  finish({ preview: null })
  await linkPreview('https://late.invalid')
  expect(api.previewLink).toHaveBeenCalledTimes(2)

  let summaryFinish!: (value: Awaited<ReturnType<typeof api.summarizeLinks>>) => void
  vi.mocked(api.summarizeLinks).mockImplementationOnce(
    () =>
      new Promise((resolve) => {
        summaryFinish = resolve
      }),
  )
  const summary = linkSummary('https://late-summary.invalid')
  await vi.waitFor(() => expect(api.summarizeLinks).toHaveBeenCalledTimes(1))
  clearLinkCaches()
  expect(await summary).toBeNull()
  expect(vi.mocked(api.summarizeLinks).mock.calls[0]![1]!.aborted).toBe(true)
  summaryFinish({ links: {} })
  await linkSummary('https://late-summary.invalid')
  expect(api.summarizeLinks).toHaveBeenCalledTimes(2)
})
