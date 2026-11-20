import type { MessageView } from '@ariadne/core'
import { ExpiringCache } from '@ariadne/core/cache'
import { onMounted, onScopeDispose, shallowRef, watch } from 'vue'
import { api } from '../api'
import { currentProjectIsIdle, returnedToProject } from '../presence'
import type { ConversationState } from '../store'

type Reply = Pick<MessageView, 'author' | 'id'>
const latestReply = (messages: readonly Reply[]) =>
  messages.findLast((message) => message.author === 'coordinator')?.id ?? ''

export function coordinatorReading(readThrough: string, messages: readonly Reply[]) {
  return {
    seen: readThrough,
    latest: readThrough || latestReply(messages),
    boundary: '',
    departed: !!readThrough,
    submitted: readThrough,
  }
}
type Reading = ReturnType<typeof coordinatorReading>

export function leaveCoordinator(reading: Reading) {
  if (reading.seen >= reading.latest) reading.departed = true
}

/** An unread batch keeps its first reply even once the whole batch has been read. */
export function receiveCoordinatorReplies(
  reading: Reading,
  messages: readonly Reply[],
  away: boolean,
) {
  const latest = latestReply(messages)
  if (latest <= reading.latest) return
  if (away) leaveCoordinator(reading)
  const firstBatch = !reading.boundary && !!reading.seen && reading.seen >= reading.latest
  if (reading.departed || firstBatch) {
    const first = messages.find(
      (message) =>
        message.author === 'coordinator' &&
        message.id > reading.latest &&
        message.id > reading.seen,
    )
    if (first) {
      reading.boundary = first.id
      reading.departed = false
    }
  }
  reading.latest = latest
}

const readingHistory = new ExpiringCache<string, Reading>(128, 24 * 60 * 60_000)

export function useCoordinatorUnread(
  conversation: () => ConversationState,
  viewport: () => HTMLElement | null | undefined,
  active: () => boolean,
) {
  const boundary = shallowRef('')
  let id = ''
  let reading: Reading | undefined
  let frame = 0

  function present() {
    return (
      active() &&
      document.visibilityState === 'visible' &&
      document.hasFocus() &&
      !currentProjectIsIdle()
    )
  }

  function canRead() {
    const element = viewport()
    return (
      present() &&
      !!element?.clientHeight &&
      element.scrollHeight - element.scrollTop - element.clientHeight <= 24
    )
  }

  function scheduleRead() {
    cancelAnimationFrame(frame)
    // ChatList follows the tail in its resize observer. Inspect the settled scroll position.
    frame = requestAnimationFrame(() => {
      frame = requestAnimationFrame(() => {
        if (!reading || !reading.latest || !canRead()) return
        reading.seen = reading.latest > reading.seen ? reading.latest : reading.seen
        if (reading.latest <= reading.submitted) return
        const progress = reading
        const previous = progress.submitted
        const latest = (progress.submitted = progress.latest)
        void api.markRead(id, latest).catch(() => {
          if (progress.submitted === latest) progress.submitted = previous
        })
      })
    })
  }

  function leave() {
    if (!reading) return
    leaveCoordinator(reading)
    readingHistory.set(id, reading)
  }

  function visibilityChanged() {
    if (present()) scheduleRead()
    else leave()
  }

  watch(
    [() => conversation().conversation?.id, () => latestReply(conversation().messages)],
    ([nextId]) => {
      if (nextId !== id) {
        leave()
        id = nextId ?? ''
        const current = conversation()
        reading = id
          ? (readingHistory.get(id) ??
            coordinatorReading(current.readThrough ?? '', current.messages))
          : undefined
      }
      if (reading) receiveCoordinatorReplies(reading, conversation().messages, !present())
      boundary.value = reading?.boundary ?? ''
      scheduleRead()
    },
    { immediate: true },
  )
  watch(active, visibilityChanged)
  watch(
    returnedToProject,
    (event) => {
      if (event?.projectId === conversation().conversation?.projectId) {
        leave()
        scheduleRead()
      }
    },
    { flush: 'sync' },
  )

  onMounted(() => {
    window.addEventListener('focus', visibilityChanged)
    window.addEventListener('blur', visibilityChanged)
    document.addEventListener('visibilitychange', visibilityChanged)
  })
  onScopeDispose(() => {
    window.removeEventListener('focus', visibilityChanged)
    window.removeEventListener('blur', visibilityChanged)
    document.removeEventListener('visibilitychange', visibilityChanged)
    cancelAnimationFrame(frame)
    leave()
  })
  function onBottomChange(atBottom: boolean) {
    if (atBottom) scheduleRead()
  }
  return { boundary, onBottomChange }
}
