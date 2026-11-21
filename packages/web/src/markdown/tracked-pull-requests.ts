import type { ThreadPullRequest, ThreadView } from '@ariadne/core'
import { computed } from 'vue'
import { state } from '../store'

/** Plan nodes carry the state and check summary without the title or commit. */
export interface PullRequestThread {
  id: string
  pullRequest:
    | (Pick<ThreadPullRequest, 'number' | 'reference' | 'url' | 'state' | 'conflicted' | 'checks'> &
        Partial<Pick<ThreadPullRequest, 'title' | 'headSha'>>)
    | null
}

export const pullRequestLabel = (pr: Pick<ThreadPullRequest, 'number' | 'reference'>) =>
  pr.reference ?? `#${pr.number}`

function addressKey(href: string) {
  try {
    const url = new URL(href)
    if (url.protocol !== 'https:' || url.username || url.password) return null
    const path = url.pathname.replace(/\/+$/, '')
    return url.origin + (url.hostname === 'github.com' ? path.toLowerCase() : path)
  } catch {
    return null
  }
}

const tracked = computed(() => {
  const byThread = new Map<string, ThreadView>()
  const byAddress = new Map<string, ThreadView>()
  for (const thread of state.detail?.threads ?? []) {
    if (!thread.pullRequest) continue
    byThread.set(thread.id, thread)
    const address = addressKey(thread.pullRequest.url)
    if (address && !byAddress.has(address)) byAddress.set(address, thread)
  }
  return { byThread, byAddress }
})

export const trackedPullRequestForThread = (threadId: string) =>
  tracked.value.byThread.get(threadId)

export function trackedPullRequestForUrl(href: string) {
  const address = addressKey(href)
  return address ? tracked.value.byAddress.get(address) : undefined
}
