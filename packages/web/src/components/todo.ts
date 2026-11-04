import type { TodoStatus, TodoView } from '@ariadne/core'
import type { ChatPlanItem, ChatPlanItemStatus } from 'dou-ui'

const status: Record<TodoStatus, ChatPlanItemStatus> = {
  done: 'done',
  doing: 'running',
  waiting: 'waiting',
  next: 'pending',
}

/**
 * A todo list as a checklist, in the order the agent wrote it. Whether the running step
 * moves is up to the plan's `live`: between turns it stays the current step, but still.
 */
export function todoPlan(todo: TodoView | null): ChatPlanItem[] {
  return (todo?.items ?? []).map((item, index) => ({
    key: index,
    label: item.label,
    status: status[item.status],
    ...(item.detail ? { description: item.detail } : {}),
  }))
}
