import type { TodoStatus, TodoView } from '@ariadne/core'
import type { ChatPlanItem, ChatPlanItemStatus } from 'dou-ui'
import { t } from '../i18n'

const status: Record<TodoStatus, ChatPlanItemStatus> = {
  done: 'done',
  doing: 'running',
  // ChatPlan has no "waiting on something" state yet; the detail line says what it waits on.
  waiting: 'pending',
  next: 'pending',
}

/**
 * A todo list as a checklist, in the order the agent wrote it. A step only shows as running
 * while a turn runs; between turns nothing is moving.
 */
export function todoPlan(todo: TodoView | null, working = true): ChatPlanItem[] {
  return (todo?.items ?? []).map((item, index) => ({
    key: index,
    label: item.label,
    status: item.status === 'doing' && !working ? 'pending' : status[item.status],
    ...(item.status === 'waiting'
      ? {
          description: item.detail
            ? t('todo.waitingOn', { detail: item.detail })
            : t('todo.waiting'),
        }
      : item.detail
        ? { description: item.detail }
        : {}),
  }))
}
