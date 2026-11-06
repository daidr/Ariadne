import type { TodoStatus, TodoView } from '@ariadne/core'
import type { ChatPlanItem, ChatPlanItemStatus } from 'dou-ui'

const status: Record<TodoStatus, ChatPlanItemStatus> = {
  done: 'done',
  doing: 'running',
  waiting: 'waiting',
  next: 'pending',
}

/**
 * A todo list as a checklist, in the order the agent wrote it. Between turns nothing is
 * under way, so the step it was on reads as not started rather than as still running.
 */
export function todoPlan(todo: TodoView | null, working = true): ChatPlanItem[] {
  return (todo?.items ?? []).map((item, index) => ({
    key: index,
    label: item.label,
    status: item.status === 'doing' && !working ? 'pending' : status[item.status],
    ...(item.detail ? { description: item.detail } : {}),
  }))
}
