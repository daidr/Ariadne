import type { AnyStoredEvent, ThreadView } from '@ariadne/core'
import { reactive } from 'vue'
import { api } from './api'
import { t, tm } from './i18n'
import { openProject, openThread, state } from './store'

const KEY = 'ariadne.notify'

type Permission = NotificationPermission | 'unsupported'

const supported = () => typeof Notification !== 'undefined'

function readEnabled(): boolean {
  try {
    return localStorage.getItem(KEY) === 'on'
  } catch {
    return false
  }
}

/** Whether the user turned browser notifications on here, and what the browser allows. */
export const notifications = reactive<{ enabled: boolean; permission: Permission }>({
  enabled: readEnabled(),
  permission: supported() ? Notification.permission : 'unsupported',
})

/** Turn notifications on (asking the browser first) or off; remembered in this browser. */
export async function setNotifications(on: boolean) {
  if (on && supported() && Notification.permission === 'default') {
    notifications.permission = await Notification.requestPermission()
  }
  notifications.enabled = on && notifications.permission === 'granted'
  try {
    localStorage.setItem(KEY, notifications.enabled ? 'on' : 'off')
  } catch {
    // Not remembered; it is off again next time.
  }
}

/** What an event asks of the user, worth a notification; `null` for the rest. */
function reasonOf(
  event: AnyStoredEvent,
  coordinatorId: string | undefined,
): { threadId: string | null; title: string; body: string } | null {
  switch (event.type) {
    case 'attention.opened':
      return {
        threadId: event.data.threadId,
        title: t('notify.answer'),
        body: event.data.question,
      }
    case 'thread.reported': {
      const { report, summary, threadId } = event.data
      if (report === 'ready') return { threadId, title: t('notify.review'), body: summary }
      if (report === 'done') return { threadId, title: t('notify.done'), body: summary }
      if (report === 'blocked' || report === 'failed') {
        return { threadId, title: t('notify.blocked'), body: summary }
      }
      return null
    }
    case 'turn.finished':
      if (event.data.outcome !== 'failed') return null
      return {
        threadId: event.data.conversationId === coordinatorId ? null : event.data.conversationId,
        title: t('notify.failed'),
        body: event.data.error ? tm(event.data.error) : '',
      }
    default:
      return null
  }
}

async function threadOf(projectId: string, threadId: string): Promise<ThreadView | null> {
  const known = state.detail?.project.id === projectId ? state.detail.threads : null
  const threads = known ?? (await api.getProject(projectId).catch(() => null))?.threads ?? []
  return threads.find((thread) => thread.id === threadId) ?? null
}

/**
 * Tell the user, while the page is out of sight, that something needs them or finished.
 * One notification per thread at a time: a newer one replaces it.
 */
export function notifyAbout(event: AnyStoredEvent) {
  if (!notifications.enabled || !supported() || Notification.permission !== 'granted') return
  if (document.visibilityState === 'visible' && document.hasFocus()) return
  const project = state.projects.find((p) => p.id === event.projectId)
  const reason = reasonOf(event, project?.coordinatorId)
  if (!reason) return
  void (async () => {
    const thread = reason.threadId ? await threadOf(event.projectId, reason.threadId) : null
    const where = [
      project?.name,
      thread?.title ?? (reason.threadId ? null : t('coordinator.title')),
    ]
      .filter(Boolean)
      .join(' · ')
    const notification = new Notification(`${reason.title} · ${where}`, {
      body: reason.body.slice(0, 200),
      tag: `ariadne:${reason.threadId ?? event.projectId}`,
    })
    notification.addEventListener('click', () => {
      window.focus()
      notification.close()
      void (async () => {
        if (state.currentId !== event.projectId) await openProject(event.projectId)
        if (reason.threadId) await openThread(reason.threadId)
      })()
    })
  })()
}
