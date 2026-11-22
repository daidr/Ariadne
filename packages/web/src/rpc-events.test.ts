// @vitest-environment happy-dom
import { create } from '@bufbuild/protobuf'
import {
  BackgroundCommandsUpdateSchema,
  BackgroundCommandViewSchema,
  LiveCommandSchema,
  LiveCommandUpdateSchema,
  ReplyDraftSchema,
  ReplyDraftUpdateSchema,
  StoredEventSchema,
  StreamEventsResponseSchema,
  StreamHeartbeatSchema,
  StreamReadySchema,
  UpdatePreferencesSchema,
  UpdateReleaseSchema,
  UpdateStatusSchema,
  type StreamEventsResponse,
} from '@ariadne/proto'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { defineComponent, h, nextTick } from 'vue'
import { api, followEvents, type StreamStatus, type UpdateStatus } from './api'
import { useUpdatesSettings } from './components/useUpdatesSettings'
import { start, state } from './store'

const encoder = new TextEncoder()

afterEach(() => {
  vi.restoreAllMocks()
  vi.useRealTimers()
  state.stream = 'connecting'
})

describe('ConnectRPC EventService.StreamEvents client', () => {
  test('dispatches ready, durable event, live, background, draft, heartbeat, and updateStatus frames', async () => {
    const frames: StreamEventsResponse[] = [
      create(StreamEventsResponseSchema, {
        payload: { case: 'ready', value: create(StreamReadySchema, { lastSeq: 10n }) },
      }),
      create(StreamEventsResponseSchema, {
        payload: {
          case: 'heartbeat',
          value: create(StreamHeartbeatSchema, { serverTimeMs: 1_700_000_000_000n }),
        },
      }),
      create(StreamEventsResponseSchema, {
        payload: {
          case: 'event',
          value: create(StoredEventSchema, {
            seq: 11n,
            id: 'evt_11',
            projectId: 'prj_1',
            type: 'project.renamed',
            at: 1_700_000_000_100n,
            rawJson: encoder.encode(JSON.stringify({ name: 'Renamed' })),
          }),
        },
      }),
      create(StreamEventsResponseSchema, {
        payload: {
          case: 'live',
          value: create(LiveCommandUpdateSchema, {
            projectId: 'prj_1',
            conversationId: 'thr_1',
            command: create(LiveCommandSchema, {
              projectId: 'prj_1',
              conversationId: 'thr_1',
              command: 'go test ./...',
              cwd: '/repo',
              startedAt: 100n,
              tail: 'ok',
            }),
          }),
        },
      }),
      create(StreamEventsResponseSchema, {
        payload: {
          case: 'background',
          value: create(BackgroundCommandsUpdateSchema, {
            projectId: 'prj_1',
            conversationId: 'thr_1',
            commands: [
              create(BackgroundCommandViewSchema, {
                id: 'cmd_1',
                command: 'bun run dev',
                startedAt: 200n,
                url: 'http://localhost:5173',
              }),
            ],
          }),
        },
      }),
      create(StreamEventsResponseSchema, {
        payload: {
          case: 'draft',
          value: create(ReplyDraftUpdateSchema, {
            projectId: 'prj_1',
            conversationId: 'thr_1',
            draft: create(ReplyDraftSchema, {
              messageId: 'msg_1',
              text: 'Working on it',
              blocksJson: encoder.encode(JSON.stringify([{ type: 'steps', items: [] }])),
              startedAt: 300n,
              posted: false,
            }),
          }),
        },
      }),
      create(StreamEventsResponseSchema, {
        payload: {
          case: 'updateStatus',
          value: create(UpdateStatusSchema, {
            version: '1.0.0',
            preferences: create(UpdatePreferencesSchema, { channel: 'stable', automatic: true }),
            canInstall: true,
            state: 'downloading',
            downloadPercent: 64,
            release: create(UpdateReleaseSchema, {
              version: '1.1.0',
              notes: 'Notes',
              url: 'https://example.com',
              prerelease: false,
              installable: true,
            }),
          }),
        },
      }),
    ]

    vi.spyOn(api, 'streamEvents').mockImplementationOnce((_opts, signal) =>
      (async function* () {
        for (const frame of frames) yield frame
        await new Promise<void>((resolve) => {
          signal?.addEventListener('abort', () => resolve(), { once: true })
        })
      })(),
    )

    const events: unknown[] = []
    const statuses: StreamStatus[] = []
    const onOpen = vi.fn()
    const onLive = vi.fn()
    const onBackground = vi.fn()
    const onDraft = vi.fn()
    const onUpdateStatus = vi.fn()

    const stop = followEvents(
      10,
      (ev) => events.push(ev),
      (status) => statuses.push(status),
      onOpen,
      onLive,
      onBackground,
      onDraft,
      onUpdateStatus,
    )

    await flushPromises()
    stop()

    expect(statuses).toEqual(['open'])
    expect(onOpen).toHaveBeenCalledTimes(1)
    expect(events).toEqual([
      {
        seq: 11,
        id: 'evt_11',
        projectId: 'prj_1',
        type: 'project.renamed',
        at: 1_700_000_000_100,
        data: { name: 'Renamed' },
      },
    ])
    expect(onLive).toHaveBeenCalledWith({
      projectId: 'prj_1',
      conversationId: 'thr_1',
      command: {
        projectId: 'prj_1',
        conversationId: 'thr_1',
        command: 'go test ./...',
        cwd: '/repo',
        startedAt: 100,
        tail: 'ok',
      },
    })
    expect(onBackground).toHaveBeenCalledWith({
      projectId: 'prj_1',
      conversationId: 'thr_1',
      commands: [
        {
          id: 'cmd_1',
          command: 'bun run dev',
          startedAt: 200,
          url: 'http://localhost:5173',
          result: null,
        },
      ],
    })
    expect(onDraft).toHaveBeenCalledWith({
      projectId: 'prj_1',
      conversationId: 'thr_1',
      draft: {
        messageId: 'msg_1',
        text: 'Working on it',
        blocks: [{ type: 'steps', items: [] }],
        startedAt: 300,
      },
    })
    expect(onUpdateStatus).toHaveBeenCalledWith({
      version: '1.0.0',
      preferences: { channel: 'stable', automatic: true },
      canInstall: true,
      state: 'downloading',
      downloadPercent: 64,
      release: {
        version: '1.1.0',
        notes: 'Notes',
        url: 'https://example.com',
        prerelease: false,
        installable: true,
      },
    })
  })

  test('reconnects with exponential backoff and resumes from the last observed seq', async () => {
    vi.useFakeTimers()
    const seenCursors: (number | undefined)[] = []
    let attempt = 0
    vi.spyOn(api, 'streamEvents').mockImplementation((opts, signal) => {
      seenCursors.push(opts.afterSeq)
      attempt++
      if (attempt === 1) {
        return (async function* () {
          yield create(StreamEventsResponseSchema, {
            payload: { case: 'ready', value: create(StreamReadySchema, { lastSeq: 5n }) },
          })
          yield create(StreamEventsResponseSchema, {
            payload: {
              case: 'event',
              value: create(StoredEventSchema, {
                seq: 8n,
                id: 'evt_8',
                projectId: 'prj_1',
                type: 'project.renamed',
                at: 10n,
                rawJson: encoder.encode('{}'),
              }),
            },
          })
          throw new TypeError('connection reset')
        })()
      }
      return (async function* () {
        yield create(StreamEventsResponseSchema, {
          payload: { case: 'ready', value: create(StreamReadySchema, { lastSeq: 8n }) },
        })
        await new Promise<void>((resolve) => {
          signal?.addEventListener('abort', () => resolve(), { once: true })
        })
      })()
    })

    const statuses: StreamStatus[] = []
    const stop = followEvents(null, vi.fn(), (s) => statuses.push(s))

    await flushPromises()
    expect(statuses).toEqual(['open', 'lost'])
    expect(seenCursors).toEqual([undefined])

    await vi.advanceTimersByTimeAsync(500)
    await flushPromises()
    expect(statuses).toEqual(['open', 'lost', 'open'])
    expect(seenCursors).toEqual([undefined, 8])

    stop()
  })

  test('useUpdatesSettings receives live update progress from EventService without timer polling', async () => {
    vi.useFakeTimers()
    const initial: UpdateStatus = {
      version: '1.0.0',
      preferences: { channel: 'stable', automatic: false },
      canInstall: true,
      state: 'downloading',
      downloadPercent: 10,
      release: {
        version: '1.1.0',
        notes: 'Notes',
        url: 'https://example.com',
        prerelease: false,
        installable: true,
      },
    }
    const statusSpy = vi.spyOn(api, 'updateStatus').mockResolvedValue(initial)
    vi.spyOn(api, 'listProjects').mockResolvedValue({ projects: [], lastSeq: 1 })
    vi.spyOn(api, 'getModelSettings').mockResolvedValue({
      models: [],
      providers: [],
      roles: { thread: { model: '' } },
      showCost: false,
    } as never)
    vi.spyOn(api, 'getInputSettings').mockResolvedValue({
      submitOn: 'enter',
      snippets: [],
      keys: {},
    } as never)

    let pushFrame!: (frame: StreamEventsResponse) => void
    vi.spyOn(api, 'streamEvents').mockImplementationOnce((_opts, signal) =>
      (async function* () {
        yield create(StreamEventsResponseSchema, {
          payload: { case: 'ready', value: create(StreamReadySchema, { lastSeq: 1n }) },
        })
        for (;;) {
          if (signal?.aborted) return
          const frame = await new Promise<StreamEventsResponse | null>((resolve) => {
            pushFrame = resolve
            signal?.addEventListener('abort', () => resolve(null), { once: true })
          })
          if (!frame) return
          yield frame
        }
      })(),
    )

    await start(async () => {})
    await flushPromises()

    let updates!: ReturnType<typeof useUpdatesSettings>
    const wrapper = mount(
      defineComponent({
        setup() {
          updates = useUpdatesSettings()
          return () => h('div')
        },
      }),
    )

    await flushPromises()
    expect(statusSpy).toHaveBeenCalledTimes(1)
    expect(updates.status.value?.downloadPercent).toBe(10)

    // Advance time significantly: no polling requests should be sent.
    await vi.advanceTimersByTimeAsync(10_000)
    expect(statusSpy).toHaveBeenCalledTimes(1)

    // Pushed progress update arrives over the ConnectRPC event stream.
    pushFrame(
      create(StreamEventsResponseSchema, {
        payload: {
          case: 'updateStatus',
          value: create(UpdateStatusSchema, {
            version: '1.0.0',
            preferences: create(UpdatePreferencesSchema, { channel: 'stable', automatic: false }),
            canInstall: true,
            state: 'downloading',
            downloadPercent: 85,
            release: create(UpdateReleaseSchema, {
              version: '1.1.0',
              notes: 'Notes',
              url: 'https://example.com',
              prerelease: false,
              installable: true,
            }),
          }),
        },
      }),
    )
    await flushPromises()
    await nextTick()
    expect(updates.status.value?.downloadPercent).toBe(85)
    expect(statusSpy).toHaveBeenCalledTimes(1)

    wrapper.unmount()
  })

  test('useUpdatesSettings keeps newer pushed status when an in-flight RPC resolves with stale data', async () => {
    const available: UpdateStatus = {
      version: '1.0.0',
      preferences: { channel: 'stable', automatic: false },
      canInstall: true,
      state: 'available',
      downloadPercent: 0,
      release: {
        version: '1.1.0',
        notes: 'Notes',
        url: 'https://example.com',
        prerelease: false,
        installable: true,
      },
    }
    const staleDownloading: UpdateStatus = {
      ...available,
      state: 'downloading',
      downloadPercent: 0,
    }

    vi.spyOn(api, 'updateStatus').mockResolvedValue(available)
    let resolveInstall!: (view: UpdateStatus) => void
    vi.spyOn(api, 'installUpdate').mockImplementation(
      () =>
        new Promise<UpdateStatus>((resolve) => {
          resolveInstall = resolve
        }),
    )
    vi.spyOn(api, 'listProjects').mockResolvedValue({ projects: [], lastSeq: 1 })
    vi.spyOn(api, 'getModelSettings').mockResolvedValue({
      models: [],
      providers: [],
      roles: { thread: { model: '' } },
      showCost: false,
    } as never)
    vi.spyOn(api, 'getInputSettings').mockResolvedValue({
      submitOn: 'enter',
      snippets: [],
      keys: {},
    } as never)

    let pushFrame!: (frame: StreamEventsResponse) => void
    vi.spyOn(api, 'streamEvents').mockImplementationOnce((_opts, signal) =>
      (async function* () {
        yield create(StreamEventsResponseSchema, {
          payload: { case: 'ready', value: create(StreamReadySchema, { lastSeq: 1n }) },
        })
        for (;;) {
          if (signal?.aborted) return
          const frame = await new Promise<StreamEventsResponse | null>((resolve) => {
            pushFrame = resolve
            signal?.addEventListener('abort', () => resolve(null), { once: true })
          })
          if (!frame) return
          yield frame
        }
      })(),
    )

    await start(async () => {})
    await flushPromises()

    let updates!: ReturnType<typeof useUpdatesSettings>
    const wrapper = mount(
      defineComponent({
        setup() {
          updates = useUpdatesSettings()
          return () => h('div')
        },
      }),
    )

    await flushPromises()
    expect(updates.status.value?.state).toBe('available')

    // Start install (unary RPC still in-flight).
    updates.install()
    expect(updates.pending.value).toBe('install')

    // Newer terminal status ('ready') arrives over EventService before installUpdate resolves.
    pushFrame(
      create(StreamEventsResponseSchema, {
        payload: {
          case: 'updateStatus',
          value: create(UpdateStatusSchema, {
            version: '1.0.0',
            preferences: create(UpdatePreferencesSchema, { channel: 'stable', automatic: false }),
            canInstall: true,
            state: 'ready',
            downloadPercent: 0,
            release: create(UpdateReleaseSchema, {
              version: '1.1.0',
              notes: 'Notes',
              url: 'https://example.com',
              prerelease: false,
              installable: true,
            }),
          }),
        },
      }),
    )
    await flushPromises()
    expect(updates.status.value?.state).toBe('ready')

    // Now the stale installUpdate RPC response arrives: must not overwrite 'ready', but must clear pending.
    resolveInstall(staleDownloading)
    await flushPromises()
    expect(updates.status.value?.state).toBe('ready')
    expect(updates.pending.value).toBeNull()

    wrapper.unmount()
  })
})
