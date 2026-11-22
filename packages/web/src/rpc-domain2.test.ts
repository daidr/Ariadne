import {
  BrowseSourceRequestSchema,
  BrowseSourceResponseSchema,
  CreateArtifactCommentResponseSchema,
  DeleteArtifactCommentResponseSchema,
  EditArtifactCommentResponseSchema,
  GetArtifactResponseSchema,
  GetIssueResponseSchema,
  GetPlanResponseSchema,
  ListArtifactsResponseSchema,
  ListArtifactVersionsResponseSchema,
  ListIssuesResponseSchema,
  ListPlanRevisionsResponseSchema,
  RemoveArtifactCommentThreadResponseSchema,
  ReopenArtifactCommentThreadResponseSchema,
  ReplyArtifactCommentResponseSchema,
  ResolveArtifactCommentThreadResponseSchema,
  UpdateProjectRequestSchema,
  UpdateProjectResponseSchema,
} from '@ariadne/proto'
import {
  create,
  fromBinary,
  toBinary,
  type DescMessage,
  type MessageInitShape,
} from '@bufbuild/protobuf'
import { describe, expect, it, vi } from 'vitest'
import { api } from './api'
import { createAriadneTransport } from './rpc'
import { createDomain2Api } from './rpc-domain2'
import { saveProjectSettings, state } from './store'

function protoResponse<Desc extends DescMessage>(
  schema: Desc,
  init: MessageInitShape<Desc>,
): Response {
  const bytes = toBinary(schema, create(schema, init))
  return new Response(new Uint8Array(bytes), {
    status: 200,
    headers: {
      'content-type': 'application/proto',
      'X-Ariadne-Server-Time': String(Date.now()),
    },
  })
}

describe('Domain 2 ConnectRPC client (Projects, Plans, Artifacts, Issues)', () => {
  it('sends atomic UpdateProject RPC with combined settings, env, slots, mcp and autopilotPaused (D-4)', async () => {
    let capturedUrl = ''
    let capturedBody: Uint8Array | null = null

    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      capturedUrl = String(input)
      const body = init?.body
      if (body instanceof Uint8Array) {
        capturedBody = body
      } else if (body instanceof ArrayBuffer) {
        capturedBody = new Uint8Array(body)
      }
      return protoResponse(UpdateProjectResponseSchema, {
        project: {
          id: 'prj_1',
          name: 'Atomic Project',
          unnamed: false,
          coordinatorId: 'conv_1',
          createdAt: 1000n,
          updatedAt: 2000n,
          needsMe: 0,
          slots: 3,
          mcp: ['srv_1'],
          settings: {
            instructions: 'Be concise',
            delivery: { mode: 'merge', instructions: '' },
            tools: ['fetch'],
            budget: { thread: 5000 },
            workspace: {
              retention: 'auto',
              auto: { setup: true, check: false },
              setup: '',
              shell: 'auto',
              check: '',
              branchPrefix: 'feat/',
            },
            routines: [],
            review: { enabled: true },
            autopilot: { enabled: true, method: 'squash' },
          },
          detected: {},
          autopilotPaused: true,
          usage: { input: 10n, output: 20n, cached: 0n, cacheWrite: 0n, reasoning: 0n, cost: {} },
        },
        settingsView: {
          settings: {
            instructions: 'Be concise',
            delivery: { mode: 'merge', instructions: '' },
            tools: ['fetch'],
            budget: { thread: 5000 },
            workspace: {
              retention: 'auto',
              auto: { setup: true, check: false },
              setup: '',
              shell: 'auto',
              check: '',
              branchPrefix: 'feat/',
            },
            routines: [],
            review: { enabled: true },
            autopilot: { enabled: true, method: 'squash' },
          },
          detected: {},
          env: ['NODE_ENV'],
          tools: ['fetch', 'search'],
          hostedMerge: true,
        },
      })
    })

    const transport = createAriadneTransport({
      baseUrl: 'http://ariadne.test/api/rpc',
      getToken: () => 'tok',
      fetch: fetchMock,
    })
    const client = createDomain2Api(transport)

    const result = await client.updateProject('prj_1', {
      name: 'Atomic Project',
      settings: {
        instructions: 'Be concise',
        delivery: { mode: 'merge', instructions: '' },
        tools: ['fetch'],
        budget: { thread: 5000, project: null },
        workspace: {
          retention: 'auto',
          auto: { setup: true, check: false },
          setup: '',
          shell: 'auto',
          check: '',
          branchPrefix: 'feat/',
        },
        routines: [],
        review: { enabled: true },
        autopilot: { enabled: true, method: 'squash' },
      },
      env: [{ name: 'NODE_ENV', value: 'test' }],
      slots: 3,
      mcp: ['srv_1'],
      autopilotPaused: true,
    })

    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(capturedUrl).toBe('http://ariadne.test/api/rpc/ariadne.v1.ProjectService/UpdateProject')
    expect(capturedBody).not.toBeNull()
    const decodedReq = fromBinary(UpdateProjectRequestSchema, capturedBody!)
    expect(decodedReq.projectId).toBe('prj_1')
    expect(decodedReq.name).toBe('Atomic Project')
    expect(decodedReq.slotsAction).toEqual({ case: 'setSlots', value: 3 })
    expect(decodedReq.mcpServers?.values).toEqual(['srv_1'])
    expect(decodedReq.autopilotPaused).toBe(true)
    expect(decodedReq.env?.entries).toHaveLength(1)

    expect(result.project.slots).toBe(3)
    expect(result.project.mcp).toEqual(['srv_1'])
    expect(result.project.detected).toEqual({ setup: null, check: null })
    expect(result.env).toEqual(['NODE_ENV'])
    expect(result.hostedMerge).toBe(true)
  })

  it('dispatches all 7 split artifact comment lifecycle RPCs (D-5)', async () => {
    const calledProcedures: string[] = []
    const sampleThread = {
      id: 'ct_1',
      artifactId: 'art_1',
      quote: 'hello',
      anchor: { target: { case: 'node' as const, value: 'n1' }, block: 'b1' },
      comments: [{ id: 'cm_1', author: 'user', body: 'note', createdAt: 100n }],
      createdAt: 100n,
    }

    const transport = createAriadneTransport({
      baseUrl: 'http://ariadne.test/api/rpc',
      getToken: () => 'tok',
      fetch: async (input) => {
        const url = String(input)
        const procedure = url.split('/').pop() ?? ''
        calledProcedures.push(procedure)
        switch (procedure) {
          case 'CreateArtifactComment':
            return protoResponse(CreateArtifactCommentResponseSchema, { comments: [sampleThread] })
          case 'ReplyArtifactComment':
            return protoResponse(ReplyArtifactCommentResponseSchema, { comments: [sampleThread] })
          case 'EditArtifactComment':
            return protoResponse(EditArtifactCommentResponseSchema, { comments: [sampleThread] })
          case 'DeleteArtifactComment':
            return protoResponse(DeleteArtifactCommentResponseSchema, { comments: [sampleThread] })
          case 'ResolveArtifactCommentThread':
            return protoResponse(ResolveArtifactCommentThreadResponseSchema, {
              comments: [{ ...sampleThread, resolved: { by: 'user', at: 200n } }],
            })
          case 'ReopenArtifactCommentThread':
            return protoResponse(ReopenArtifactCommentThreadResponseSchema, {
              comments: [sampleThread],
            })
          case 'RemoveArtifactCommentThread':
            return protoResponse(RemoveArtifactCommentThreadResponseSchema, { comments: [] })
          default:
            throw new Error(`unexpected procedure ${procedure}`)
        }
      },
    })
    const client = createDomain2Api(transport)

    const created = await client.createArtifactComment('art_1', {
      threadId: 'ct_1',
      commentId: 'cm_1',
      quote: 'hello',
      body: 'note',
      anchor: { node: 'n1', block: 'b1' },
    })
    expect(created.comments[0]?.anchor).toEqual({ node: 'n1', block: 'b1' })

    await client.replyArtifactComment('art_1', { threadId: 'ct_1', commentId: 'cm_2', body: 're' })
    await client.editArtifactComment('art_1', { threadId: 'ct_1', commentId: 'cm_2', body: 'ed' })
    const resolved = await client.resolveArtifactCommentThread('art_1', 'ct_1')
    expect(resolved.comments[0]?.resolved).toEqual({ by: 'user', at: 200 })
    await client.reopenArtifactCommentThread('art_1', 'ct_1')
    await client.deleteArtifactComment('art_1', { threadId: 'ct_1', commentId: 'cm_2' })
    const removed = await client.removeArtifactCommentThread('art_1', 'ct_1')
    expect(removed.comments).toEqual([])

    expect(calledProcedures).toEqual([
      'CreateArtifactComment',
      'ReplyArtifactComment',
      'EditArtifactComment',
      'ResolveArtifactCommentThread',
      'ReopenArtifactCommentThread',
      'DeleteArtifactComment',
      'RemoveArtifactCommentThread',
    ])
  })

  it('supports cursor pagination on artifacts, versions, and plan revisions plus issues RPCs (P-6)', async () => {
    const transport = createAriadneTransport({
      baseUrl: 'http://ariadne.test/api/rpc',
      getToken: () => 'tok',
      fetch: async (input) => {
        const procedure = String(input).split('/').pop() ?? ''
        switch (procedure) {
          case 'ListArtifacts':
            return protoResponse(ListArtifactsResponseSchema, {
              artifacts: [
                {
                  id: 'art_1',
                  projectId: 'prj_1',
                  kind: 'document',
                  title: 'Spec',
                  version: 3n,
                  createdBy: 'user',
                  updatedBy: 'user',
                  createdAt: 100n,
                  updatedAt: 200n,
                  preview: { preview: { case: 'document', value: { excerpt: 'ex', lead: 'ld' } } },
                  openComments: 1n,
                  commentedAt: 150n,
                },
              ],
              nextPageToken: 'tok_art_2',
              hasMore: true,
            })
          case 'ListArtifactVersions':
            return protoResponse(ListArtifactVersionsResponseSchema, {
              versions: [
                { version: 3n, title: 'Spec', author: 'user', reason: 'v3', createdAt: 200n },
              ],
              nextPageToken: 'tok_ver_2',
              hasMore: true,
            })
          case 'GetArtifact':
            return protoResponse(GetArtifactResponseSchema, {
              artifact: {
                id: 'art_1',
                projectId: 'prj_1',
                kind: 'document',
                title: 'Spec',
                version: 3n,
                createdBy: 'user',
                updatedBy: 'user',
                createdAt: 100n,
                updatedAt: 200n,
                content: '# Hello',
                openComments: 0n,
                commentedAt: 0n,
              },
              versions: [],
              comments: [],
              nextVersionPageToken: '',
              hasMoreVersions: false,
            })
          case 'GetPlan':
            return protoResponse(GetPlanResponseSchema, {
              plan: {
                projectId: 'prj_1',
                nodes: [
                  {
                    id: 'n1',
                    kind: 'task',
                    title: 'Grouped task',
                    description: '',
                    acceptance: '',
                    group: 'Phase 1',
                    dependsOn: [],
                    status: 'none',
                    threads: [],
                    issues: [],
                  },
                ],
                revisions: [],
                proposals: [],
              },
              nextRevisionPageToken: 'tok_rev_2',
              hasMoreRevisions: true,
            })
          case 'ListPlanRevisions':
            return protoResponse(ListPlanRevisionsResponseSchema, {
              revisions: [
                { id: 'rev_1', author: 'user', reason: 'init', ops: [], createdAt: 100n },
              ],
              nextPageToken: '',
              hasMore: false,
            })
          case 'ListIssues':
            return protoResponse(ListIssuesResponseSchema, {
              tracker: 'github',
              webUrl: 'https://github.com/org/repo',
              issues: [
                {
                  number: 42n,
                  url: 'https://github.com/org/repo/issues/42',
                  title: 'Migrate RPC',
                  state: 'open',
                  author: 'dev',
                  labels: ['rpc'],
                  comments: 3,
                  updatedAt: '2026-11-22T00:00:00Z',
                },
              ],
            })
          case 'GetIssue':
            return protoResponse(GetIssueResponseSchema, {
              tracker: 'github',
              webUrl: 'https://github.com/org/repo',
              issue: {
                summary: {
                  number: 42n,
                  url: 'https://github.com/org/repo/issues/42',
                  title: 'Migrate RPC',
                  state: 'open',
                  author: 'dev',
                  labels: ['rpc'],
                  comments: 1,
                  updatedAt: '2026-11-22T00:00:00Z',
                },
                body: 'Details',
                createdAt: '2026-11-20T00:00:00Z',
                recent: [],
              },
            })
          default:
            throw new Error(`unexpected procedure ${procedure}`)
        }
      },
    })
    const client = createDomain2Api(transport)

    const arts = await client.listArtifacts('prj_1', { pageSize: 1 })
    expect(arts.hasMore).toBe(true)
    expect(arts.nextPageToken).toBe('tok_art_2')
    expect(arts.artifacts[0]?.version).toBe(3)

    const vers = await client.listArtifactVersions('art_1', { pageSize: 1 })
    expect(vers.hasMore).toBe(true)
    expect(vers.nextPageToken).toBe('tok_ver_2')

    const art = await client.getArtifact('art_1', { versions: false })
    expect(art.artifact.content).toBe('# Hello')
    expect(art.versions).toEqual([])

    const plan = await client.getPlan('prj_1', { revisionPageSize: 1 })
    expect(plan.hasMoreRevisions).toBe(true)
    expect(plan.nextRevisionPageToken).toBe('tok_rev_2')
    expect(plan.plan.nodes[0]?.group).toBe('Phase 1')

    const revs = await client.listPlanRevisions('prj_1', { pageToken: 'tok_rev_2' })
    expect(revs.revisions).toHaveLength(1)
    expect(revs.hasMore).toBe(false)

    const issueList = await client.listIssues('prj_1', { state: 'open' })
    expect(issueList.issues[0]?.number).toBe(42)

    const issue = await client.getIssue('prj_1', 42)
    expect(issue.issue.body).toBe('Details')
  })

  it('saveProjectSettings in store issues a single atomic updateProject call (D-4)', async () => {
    const prevDetail = state.detail
    const prevCurrentId = state.currentId
    const baseSettings = {
      instructions: '',
      delivery: { mode: 'merge' as const, instructions: '' },
      tools: [] as string[],
      budget: { thread: null, project: null },
      workspace: {
        retention: 'auto' as const,
        auto: { setup: false, check: false },
        setup: '',
        shell: 'auto' as const,
        check: '',
        branchPrefix: '',
      },
      routines: [],
      review: { enabled: false },
      autopilot: { enabled: false, method: 'merge' as const },
    }
    const detail = {
      project: {
        id: 'prj_atomic',
        name: 'Atomic',
        unnamed: false,
        coordinatorId: 'conv_a',
        createdAt: 1,
        updatedAt: 1,
        needsMe: 0,
        source: null,
        references: [],
        slots: 2,
        mcp: ['srv_old'],
        settings: baseSettings,
        detected: { setup: null, check: null },
        autopilotPaused: false,
        usage: {
          input: 0,
          output: 0,
          cached: 0,
          cacheWrite: 0,
          reasoning: 0,
          cost: { USD: 0, CNY: 0 },
        },
      },
      threads: [],
      needsMe: [],
      hasTracker: false,
    }
    state.currentId = 'prj_atomic'
    state.detail = detail

    const updateSpy = vi.spyOn(api, 'updateProject').mockResolvedValue({
      project: detail.project,
      settings: baseSettings,
      detected: { setup: null, check: null },
      env: [],
      tools: [],
      hostedMerge: false,
    })
    const getProjectSpy = vi.spyOn(api, 'getProject').mockResolvedValue({
      ...detail,
      sync: { cursor: 'c1', partial: false },
    })

    try {
      const err = await saveProjectSettings(
        { settings: baseSettings, env: [{ name: 'FOO', value: 'bar' }] },
        5,
        ['srv_new'],
      )
      expect(err).toBeNull()
      expect(updateSpy).toHaveBeenCalledTimes(1)
      expect(updateSpy).toHaveBeenCalledWith('prj_atomic', {
        settings: baseSettings,
        env: [{ name: 'FOO', value: 'bar' }],
        slots: 5,
        mcp: ['srv_new'],
      })
    } finally {
      updateSpy.mockRestore()
      getProjectSpy.mockRestore()
      state.detail = prevDetail
      state.currentId = prevCurrentId
    }
  })

  it('preserves explicit empty sourceId in browseSource so new sources do not reuse primary secrets', async () => {
    const capturedRequests: Uint8Array[] = []
    const transport = createAriadneTransport({
      baseUrl: 'http://ariadne.test/api/rpc',
      getToken: () => 'tok',
      fetch: async (_input, init) => {
        const body = init?.body
        if (body instanceof Uint8Array) {
          capturedRequests.push(body)
        } else if (body instanceof ArrayBuffer) {
          capturedRequests.push(new Uint8Array(body))
        }
        return protoResponse(BrowseSourceResponseSchema, {
          listing: { path: '/', directories: [] },
        })
      },
    })
    const client = createDomain2Api(transport)

    await client.browseSource('remote', {
      projectId: 'prj_1',
      sourceId: '',
      settings: { host: 'new.example.test' },
      path: '/',
    })
    await client.browseSource('remote', {
      projectId: 'prj_1',
      settings: { host: 'primary.example.test' },
      path: '/',
    })

    expect(capturedRequests).toHaveLength(2)
    const newSourceReq = fromBinary(BrowseSourceRequestSchema, capturedRequests[0]!)
    expect(newSourceReq.projectId).toBe('prj_1')
    expect(newSourceReq.sourceId).toBe('')

    const primaryFallbackReq = fromBinary(BrowseSourceRequestSchema, capturedRequests[1]!)
    expect(primaryFallbackReq.projectId).toBe('prj_1')
    expect(primaryFallbackReq.sourceId).toBeUndefined()
  })
})
