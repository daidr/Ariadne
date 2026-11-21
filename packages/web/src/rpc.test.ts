import { DataService, GetDataUsageResponseSchema, LocalizedErrorSchema } from '@ariadne/proto'
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
})
