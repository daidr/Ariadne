import type { LocalizedMessage, MessageKey, MessageParams } from '@ariadne/i18n'
import {
  LocalizedErrorSchema,
  type ErrorParamValue,
  type LocalizedError as ProtoLocalizedError,
} from '@ariadne/proto'
import type { DescService } from '@bufbuild/protobuf'
import {
  Code,
  ConnectError,
  createClient,
  type Client,
  type Interceptor,
  type Transport,
} from '@connectrpc/connect'
import { createConnectTransport, type ConnectTransportOptions } from '@connectrpc/connect-web'
import { synchronizeServerClock } from './server-clock'

const TOKEN_KEY = 'ariadne.token'

let sessionToken: string | null = null

function decodeTokenValue(value: string): string {
  try {
    return decodeURIComponent(value.replace(/\+/g, '%20')).trim()
  } catch {
    return value.trim()
  }
}

/**
 * Check that a token only uses printable ASCII (`0x20–0x7E`), so Fetch `Headers` and HTTP
 * servers never reject `Authorization: Bearer <token>` with a ByteString / control-char error.
 */
export function isValidToken(value: string): boolean {
  return value.length > 0 && /^[\x20-\x7E]+$/.test(value)
}

/**
 * Normalize a pasted access token: accepts a raw token, a `Bearer <token>` header,
 * or the server's startup URL / hash / query containing `token=...`.
 */
export function parseTokenInput(raw: string): string {
  const trimmed = raw.trim()
  if (!trimmed) return ''
  if (/^(?:https?:\/\/|[#?]|token=)/i.test(trimmed)) {
    const match = /(?:^|[#&?])token=([^&#\s]*)/.exec(trimmed)
    return match ? decodeTokenValue(match[1] ?? '') : ''
  }
  const bearer = /^Bearer(?:\s+(.*))?$/i.exec(trimmed)
  if (bearer) return (bearer[1] ?? '').trim()
  return trimmed
}

/**
 * Persist an already-normalized access token without re-parsing it.
 * Pass `''` to clear storage; non-empty values containing invalid HTTP header characters are rejected.
 */
export function saveToken(next: string): string {
  if (next === '') {
    sessionToken = null
    try {
      localStorage.removeItem(TOKEN_KEY)
    } catch {
      // Storage can be unavailable; clearing the in-memory token still applies.
    }
    return ''
  }
  if (!isValidToken(next)) return ''
  sessionToken = next
  try {
    localStorage.setItem(TOKEN_KEY, next)
  } catch {
    // Storage can be unavailable; the token then lives only for this page.
  }
  return next
}

/** Parse and persist a manually entered access token so standalone PWA launches stay signed in. */
export function setToken(raw: string): string {
  if (raw === '') return saveToken('')
  const next = parseTokenInput(raw)
  return next ? saveToken(next) : ''
}

/**
 * The server prints a link with `#token=…`. Take the token from there once, keep it,
 * and drop it from the address bar so it does not end up in history or screenshots.
 */
export function initToken(): string | null {
  const match = /(?:^|[#&])token=([^&]+)/.exec(location.hash)
  if (match?.[1]) {
    saveToken(decodeTokenValue(match[1]))
    history.replaceState(null, '', location.pathname + location.search)
  }
  return getToken()
}

export function getToken(): string | null {
  try {
    return localStorage.getItem(TOKEN_KEY) ?? sessionToken
  } catch {
    return sessionToken
  }
}

/** Build a Header-safe `Authorization` value; invalid stored tokens fall back to empty so the server answers 401. */
export function authorizationHeader(raw: string | null = getToken()): string {
  return `Bearer ${raw && isValidToken(raw) ? raw : ''}`
}

export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly localized: LocalizedMessage,
  ) {
    super(localized.key)
  }
}

function connectCodeToHttpStatus(code: Code): number {
  switch (code) {
    case Code.InvalidArgument:
    case Code.FailedPrecondition:
    case Code.OutOfRange:
      return 400
    case Code.Unauthenticated:
      return 401
    case Code.PermissionDenied:
      return 403
    case Code.NotFound:
      return 404
    case Code.AlreadyExists:
    case Code.Aborted:
      return 409
    case Code.ResourceExhausted:
      return 429
    case Code.Unavailable:
      return 503
    default:
      return 500
  }
}

function decodeErrorParams(entries: Record<string, ErrorParamValue>): MessageParams | undefined {
  const keys = Object.keys(entries)
  if (keys.length === 0) return undefined
  const params: MessageParams = {}
  for (const key of keys) {
    const entry = entries[key]
    if (!entry) continue
    switch (entry.value.case) {
      case 'stringValue':
      case 'numberValue':
        params[key] = entry.value.value
        break
      case 'boolValue':
        params[key] = String(entry.value.value)
        break
      default:
        break
    }
  }
  return Object.keys(params).length > 0 ? params : undefined
}

export function decodeLocalizedError(detail?: ProtoLocalizedError | null): LocalizedMessage | null {
  if (!detail?.key) return null
  const params = decodeErrorParams(detail.params)
  return {
    key: detail.key as MessageKey,
    ...(params ? { params } : {}),
  }
}

export function connectErrorToApiError(error: ConnectError): ApiError {
  const status = connectCodeToHttpStatus(error.code)
  const [detail] = error.findDetails(LocalizedErrorSchema)
  const localized = decodeLocalizedError(detail)
  if (localized) {
    return new ApiError(status, localized)
  }
  if (error.code === Code.Unauthenticated) {
    return new ApiError(401, { key: 'error.unauthorized' })
  }
  if (error.code === Code.NotFound) {
    return new ApiError(404, {
      key: 'error.notFound',
      params: { what: error.rawMessage || 'resource' },
    })
  }
  return new ApiError(status, {
    key: 'error.invalidBody',
    params: { reason: error.rawMessage || 'internal' },
  })
}

export type RpcFetch = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>

export interface AriadneTransportOptions {
  baseUrl?: string
  getToken?: () => string | null
  fetch?: RpcFetch
  useBinaryFormat?: boolean
}

const clockInterceptor: Interceptor = (next) => async (req) => {
  const res = await next(req)
  synchronizeServerClock(res.header.get('X-Ariadne-Server-Time'))
  return res
}

function mapRpcError(error: unknown, signal?: AbortSignal): never {
  if (signal?.aborted) throw error
  if (error instanceof ConnectError) {
    synchronizeServerClock(error.metadata.get('X-Ariadne-Server-Time'))
    throw connectErrorToApiError(error)
  }
  throw error
}

async function* wrapStreamMessages<T>(
  messages: AsyncIterable<T>,
  signal?: AbortSignal,
): AsyncGenerator<T> {
  try {
    for await (const item of messages) {
      yield item
    }
  } catch (error) {
    mapRpcError(error, signal)
  }
}

export function createAriadneTransport(options: AriadneTransportOptions = {}): Transport {
  const readToken = options.getToken ?? getToken
  const authInterceptor: Interceptor = (next) => async (req) => {
    req.header.set('authorization', authorizationHeader(readToken()))
    return next(req)
  }
  const base = createConnectTransport({
    baseUrl: options.baseUrl ?? '/api/rpc',
    useBinaryFormat: options.useBinaryFormat ?? true,
    ...(options.fetch ? { fetch: options.fetch as ConnectTransportOptions['fetch'] } : {}),
    interceptors: [authInterceptor, clockInterceptor],
  })
  return {
    async unary(method, signal, timeoutMs, header, input, contextValues) {
      try {
        return await base.unary(method, signal, timeoutMs, header, input, contextValues)
      } catch (error) {
        mapRpcError(error, signal)
      }
    },
    async stream(method, signal, timeoutMs, header, input, contextValues) {
      try {
        const res = await base.stream(method, signal, timeoutMs, header, input, contextValues)
        return {
          ...res,
          message: wrapStreamMessages(res.message, signal),
        }
      } catch (error) {
        mapRpcError(error, signal)
      }
    },
  }
}

export const rpcTransport: Transport = createAriadneTransport()

export function createRpcClient<T extends DescService>(
  service: T,
  transport: Transport = rpcTransport,
): Client<T> {
  return createClient(service, transport)
}
