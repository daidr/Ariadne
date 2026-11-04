/** The protocol versions this client speaks, newest first; it asks for the first. */
export const PROTOCOL_VERSIONS = ['2025-06-18', '2025-03-26', '2024-11-05'] as const

export type JsonRpcId = string | number

export interface JsonRpcRequest {
  jsonrpc: '2.0'
  id: JsonRpcId
  method: string
  params?: Record<string, unknown>
}

export interface JsonRpcNotification {
  jsonrpc: '2.0'
  method: string
  params?: Record<string, unknown>
}

export interface JsonRpcResponse {
  jsonrpc: '2.0'
  id: JsonRpcId | null
  result?: unknown
  error?: { code: number; message: string; data?: unknown }
}

export type JsonRpcMessage = JsonRpcRequest | JsonRpcNotification | JsonRpcResponse

export const isResponse = (message: JsonRpcMessage): message is JsonRpcResponse =>
  !('method' in message) && 'id' in message

export const isRequest = (message: JsonRpcMessage): message is JsonRpcRequest =>
  'method' in message && 'id' in message && message.id !== undefined && message.id !== null

/** One tool a server offers, as `tools/list` describes it. */
export interface McpToolInfo {
  name: string
  title?: string
  description?: string
  /** JSON Schema of the arguments; always an object schema. */
  inputSchema: Record<string, unknown>
  annotations?: {
    readOnlyHint?: boolean
    destructiveHint?: boolean
    title?: string
  }
}

export type McpContent =
  | { type: 'text'; text: string }
  | { type: 'image'; data: string; mimeType: string }
  | { type: 'audio'; data: string; mimeType: string }
  | {
      type: 'resource_link'
      uri: string
      name?: string
      description?: string
      mimeType?: string
    }
  | {
      type: 'resource'
      resource: {
        uri: string
        mimeType?: string
        text?: string
        blob?: string
      }
    }

/** What `tools/call` returns. */
export interface McpToolResult {
  content: McpContent[]
  structuredContent?: unknown
  isError?: boolean
}

/** A JSON-RPC error the server answered with. */
export class McpRemoteError extends Error {
  constructor(
    readonly code: number,
    message: string,
  ) {
    super(message)
  }
}

/** The server could not be reached or broke the protocol; the connection is no use any more. */
export class McpConnectionError extends Error {}

/**
 * An HTTP server no longer knows the session (it restarted, or the session expired). The
 * request was not carried out; a new connection may send it again.
 */
export class McpSessionEndedError extends McpConnectionError {
  constructor() {
    super('the server ended the session')
  }
}

const isObject = (value: unknown): value is Record<string, unknown> =>
  typeof value === 'object' && value !== null && !Array.isArray(value)

/** Parse one wire message, or `null` when it is not JSON-RPC. */
export function parseMessage(text: string): JsonRpcMessage | null {
  let value: unknown
  try {
    value = JSON.parse(text)
  } catch {
    return null
  }
  if (!isObject(value) || value.jsonrpc !== '2.0') return null
  return value as unknown as JsonRpcMessage
}

/** The tools of a `tools/list` page, leaving out entries that are not tools. */
export function toolsOf(result: unknown): {
  tools: McpToolInfo[]
  nextCursor?: string
} {
  if (!isObject(result) || !Array.isArray(result.tools)) {
    throw new McpConnectionError('tools/list answered without a list of tools')
  }
  const tools = result.tools.flatMap((raw): McpToolInfo[] => {
    if (!isObject(raw) || typeof raw.name !== 'string' || !raw.name) return []
    const schema = isObject(raw.inputSchema) ? raw.inputSchema : {}
    return [
      {
        name: raw.name,
        ...(typeof raw.title === 'string' ? { title: raw.title } : {}),
        ...(typeof raw.description === 'string' ? { description: raw.description } : {}),
        inputSchema: { ...schema, type: 'object' },
        ...(isObject(raw.annotations)
          ? { annotations: raw.annotations as McpToolInfo['annotations'] }
          : {}),
      },
    ]
  })
  const nextCursor = typeof result.nextCursor === 'string' ? result.nextCursor : undefined
  return { tools, ...(nextCursor ? { nextCursor } : {}) }
}

/** A `tools/call` result, keeping only content parts it can read. */
export function toolResultOf(result: unknown): McpToolResult {
  if (!isObject(result)) throw new McpConnectionError('tools/call answered without a result')
  const content = Array.isArray(result.content)
    ? result.content.filter(
        (part): part is McpContent => isObject(part) && typeof part.type === 'string',
      )
    : []
  return {
    content,
    ...('structuredContent' in result ? { structuredContent: result.structuredContent } : {}),
    ...(result.isError === true ? { isError: true } : {}),
  }
}
