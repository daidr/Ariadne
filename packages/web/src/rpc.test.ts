import {
  DataService,
  GetDataUsageRequestSchema,
  GetDataUsageResponseSchema,
  LocalizedErrorSchema,
} from '@ariadne/proto'
import { create, toBinary } from '@bufbuild/protobuf'
import { Code, ConnectError } from '@connectrpc/connect'
import { effectScope } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  ApiError,
  connectErrorToApiError,
  createAriadneTransport,
  createRpcClient,
  type RpcFetch,
} from './rpc'
import { useServerNow } from './server-clock'

function encodeConnectEndStreamError(payload: Record<string, unknown>): Uint8Array {
  const json = new TextEncoder().encode(JSON.stringify(payload))
  const frame = new Uint8Array(5 + json.length)
  frame[0] = 0b0000_0010 // Connect end-of-stream envelope flag
  new DataView(frame.buffer).setUint32(1, json.length, false)
  frame.set(json, 5)
  return frame
}

const streamEndErrorFetch: RpcFetch = async () => {
  const detailBytes = toBinary(
    LocalizedErrorSchema,
    create(LocalizedErrorSchema, {
      key: 'error.artifactConflict',
      params: {
        currentVersion: { value: { case: 'numberValue', value: 7 } },
      },
    }),
  )
  const frame = encodeConnectEndStreamError({
    error: {
      code: 'aborted',
      message: 'error.artifactConflict',
      details: [
        {
          type: LocalizedErrorSchema.typeName,
          value: Buffer.from(detailBytes).toString('base64'),
        },
      ],
    },
    metadata: {
      'x-ariadne-server-time': ['1900000099000'],
    },
  })
  return new Response(new Uint8Array(frame), {
    status: 200,
    headers: {
      'content-type': 'application/connect+proto',
    },
  })
}

async function* emptyStreamInput() {
  yield create(GetDataUsageRequestSchema, { includeSizes: false })
}

const unauthorizedFetch: RpcFetch = async () =>
  new Response(JSON.stringify({ error: { key: 'error.unauthorized' } }), {
    status: 401,
    headers: { 'content-type': 'application/json; charset=utf-8' },
  })

describe('ConnectRPC transport and error mapping', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('sends binary protobuf with bearer auth and synchronizes server clock', async () => {
    vi.spyOn(performance, 'now').mockReturnValue(500)
    let capturedUrl = ''
    let capturedAuth = ''
    let capturedContentType = ''
    const payload = toBinary(
      GetDataUsageResponseSchema,
      create(GetDataUsageResponseSchema, {
        home: '/tmp/ariadne',
        database: 4096n,
        plugins: 1024n,
        backups: [],
        backupsDir: '/tmp/ariadne/backups',
      }),
    )
    const mockFetch: RpcFetch = async (input, init) => {
      capturedUrl =
        typeof input === 'string' ? input : input instanceof URL ? input.href : input.url
      const headers = new Headers(init?.headers)
      capturedAuth = headers.get('authorization') ?? ''
      capturedContentType = headers.get('content-type') ?? ''
      return new Response(payload, {
        status: 200,
        headers: {
          'content-type': 'application/proto',
          'x-ariadne-server-time': '1900000005000',
        },
      })
    }

    const transport = createAriadneTransport({
      baseUrl: '/api/rpc',
      getToken: () => 'secret-token',
      fetch: mockFetch,
    })
    const client = createRpcClient(DataService, transport)
    const response = await client.getDataUsage({ includeSizes: true })

    expect(capturedUrl).toBe('/api/rpc/ariadne.v1.DataService/GetDataUsage')
    expect(capturedAuth).toBe('Bearer secret-token')
    expect(capturedContentType).toBe('application/proto')
    expect(response.home).toBe('/tmp/ariadne')
    expect(response.database).toBe(4096n)
    expect(response.plugins).toBe(1024n)

    const scope = effectScope()
    const now = scope.run(() => useServerNow())
    expect(now?.value).toBe(1_900_000_005_000)
    scope.stop()
  })

  it('decodes LocalizedError details into ApiError', () => {
    const connectErr = new ConnectError('error.fileTooLarge', Code.InvalidArgument, undefined, [
      {
        desc: LocalizedErrorSchema,
        value: create(LocalizedErrorSchema, {
          key: 'error.fileTooLarge',
          params: {
            max: { value: { case: 'numberValue', value: 10 } },
            name: { value: { case: 'stringValue', value: 'demo.bin' } },
          },
        }),
      },
    ])

    const apiErr = connectErrorToApiError(connectErr)
    expect(apiErr).toBeInstanceOf(ApiError)
    expect(apiErr.status).toBe(400)
    expect(apiErr.localized).toEqual({
      key: 'error.fileTooLarge',
      params: { max: 10, name: 'demo.bin' },
    })
  })

  it('maps unauthenticated Connect responses without details to error.unauthorized', async () => {
    const transport = createAriadneTransport({
      getToken: () => null,
      fetch: unauthorizedFetch,
    })
    const client = createRpcClient(DataService, transport)

    await expect(client.getDataUsage({ includeSizes: false })).rejects.toMatchObject({
      status: 401,
      localized: { key: 'error.unauthorized' },
    })
  })

  it('maps mid-stream ConnectError during message iteration to ApiError and syncs server clock', async () => {
    vi.spyOn(performance, 'now').mockReturnValue(250)
    const transport = createAriadneTransport({
      getToken: () => 'secret-token',
      fetch: streamEndErrorFetch,
    })
    const method = DataService.method.getDataUsage
    const streamMethod = { ...method, methodKind: 'server_streaming' as const }
    const response = await transport.stream(
      streamMethod,
      undefined,
      undefined,
      undefined,
      emptyStreamInput(),
    )

    const consume = async () => {
      for await (const _ of response.message) {
        // Expect error on end-stream frame before any message.
      }
    }

    await expect(consume()).rejects.toMatchObject({
      status: 409,
      localized: {
        key: 'error.artifactConflict',
        params: { currentVersion: 7 },
      },
    })

    const scope = effectScope()
    const now = scope.run(() => useServerNow())
    expect(now?.value).toBe(1_900_000_099_000)
    scope.stop()
  })
})
