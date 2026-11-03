import { DomainError } from '@ariadne/core'
import type { LocalizedMessage } from '@ariadne/i18n'

export function json(body: unknown, status = 200): Response {
  return Response.json(body, { status })
}

export function errorResponse(error: LocalizedMessage, status: number): Response {
  return json({ error }, status)
}

/** Map domain errors to HTTP; anything else is a bug and becomes a 500. */
export function toErrorResponse(error: unknown): Response {
  if (error instanceof DomainError) {
    const status =
      error.code === 'error.notFound' ? 404 : error.code === 'error.artifactConflict' ? 409 : 400
    return errorResponse(error.toLocalized(), status)
  }
  console.error(error)
  return json({ error: { key: 'error.invalidBody', params: { reason: 'internal' } } }, 500)
}

export class BadRequest extends DomainError {
  constructor(reason: string) {
    super('error.invalidBody', { reason })
  }
}

export async function readJson(request: Request): Promise<Record<string, unknown>> {
  let body: unknown
  try {
    body = await request.json()
  } catch {
    throw new BadRequest('json')
  }
  if (typeof body !== 'object' || body === null || Array.isArray(body)) throw new BadRequest('json')
  return body as Record<string, unknown>
}

export function requireString(body: Record<string, unknown>, field: string): string {
  const value = body[field]
  if (typeof value !== 'string' || !value.trim()) throw new BadRequest(field)
  return value
}

/** Compare tokens in constant time. */
export function sameToken(a: string, b: string): boolean {
  const left = new TextEncoder().encode(a)
  const right = new TextEncoder().encode(b)
  if (left.length !== right.length) return false
  let diff = 0
  for (let i = 0; i < left.length; i++) diff |= (left[i] ?? 0) ^ (right[i] ?? 0)
  return diff === 0
}
