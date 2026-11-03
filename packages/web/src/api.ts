import type {
  AnyStoredEvent,
  AttentionView,
  MessageView,
  ProjectView,
  ThreadView,
  TodoView,
} from '@ariadne/core'
import type { LocalizedMessage } from '@ariadne/i18n'

const TOKEN_KEY = 'ariadne.token'

/**
 * The server prints a link with `#token=…`. Take the token from there once, keep it,
 * and drop it from the address bar so it does not end up in history or screenshots.
 */
export function initToken(): string | null {
  const match = /(?:^|[#&])token=([^&]+)/.exec(location.hash)
  if (match?.[1]) {
    try {
      localStorage.setItem(TOKEN_KEY, decodeURIComponent(match[1]))
    } catch {
      // Storage can be unavailable; the token then lives only for this page.
      sessionToken = decodeURIComponent(match[1])
    }
    history.replaceState(null, '', location.pathname + location.search)
  }
  return token()
}

let sessionToken: string | null = null

function token(): string | null {
  try {
    return localStorage.getItem(TOKEN_KEY) ?? sessionToken
  } catch {
    return sessionToken
  }
}

export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly localized: LocalizedMessage,
  ) {
    super(localized.key)
  }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { authorization: `Bearer ${token() ?? ''}` }
  if (body !== undefined) headers['content-type'] = 'application/json'
  const response = await fetch(path, {
    method,
    headers,
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  })
  if (response.status === 204) return undefined as T
  const data = await response.json()
  if (!response.ok) throw new ApiError(response.status, data.error)
  return data as T
}

export interface ProjectDetail {
  project: ProjectView
  threads: ThreadView[]
  attentions: AttentionView[]
}

export const api = {
  listProjects: () => request<{ projects: ProjectView[] }>('GET', '/api/projects'),
  createProject: (name: string) =>
    request<{ project: ProjectView }>('POST', '/api/projects', { name }),
  getProject: (id: string) => request<ProjectDetail>('GET', `/api/projects/${id}`),
  getConversation: (id: string) =>
    request<{ messages: MessageView[]; todo: TodoView }>('GET', `/api/conversations/${id}`),
  postMessage: (projectId: string, conversationId: string, content: string) =>
    request<{ message: MessageView }>('POST', `/api/projects/${projectId}/messages`, {
      conversationId,
      content,
    }),
  markRead: (conversationId: string, upTo: string) =>
    request<void>('POST', `/api/conversations/${conversationId}/read`, { upTo }),
  answerAttention: (id: string, choice: number) =>
    request<void>('POST', `/api/attentions/${id}/answer`, { choice }),
}

/** Follow the event log. EventSource reconnects by itself and resumes from the last id. */
export function followEvents(
  onEvent: (event: AnyStoredEvent) => void,
  onStatus: (connected: boolean) => void,
): () => void {
  const source = new EventSource(`/api/events?token=${encodeURIComponent(token() ?? '')}`)
  source.addEventListener('open', () => onStatus(true))
  source.addEventListener('error', () => onStatus(false))
  source.addEventListener('ariadne', (message) => {
    onEvent(JSON.parse((message as MessageEvent<string>).data))
  })
  return () => source.close()
}
