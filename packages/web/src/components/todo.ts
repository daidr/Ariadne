import type { MessageView, TodoStatus, TodoView } from '@ariadne/core'
import type { ChatPlanItem, ChatPlanItemStatus } from 'dou-ui'
import { relayIdOf } from '../relays'

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

type TimelinePoint = { at: number; message: Pick<MessageView, 'id'> | null }
type TodoMarker = { id: string; todo: true }

/** Place one checklist at its last structural update, once that part of the log is loaded. */
export function placeTodo<T extends TimelinePoint>(
  items: readonly T[],
  todo: TodoView | null,
  oldest?: Pick<MessageView, 'id' | 'createdAt'>,
): readonly (T | TodoMarker)[] {
  if (!todo?.items.length) return items
  const at = todo.position?.at ?? todo.updatedAt
  const afterId = todo.position?.afterMessageId
  const comesAfter = (time: number, messageId?: string) =>
    time > at ||
    (time === at &&
      messageId !== undefined &&
      afterId !== undefined &&
      (afterId === null || relayIdOf(messageId) > afterId))
  if (oldest && comesAfter(oldest.createdAt, oldest.id)) return items
  const index = items.findIndex((item) => comesAfter(item.at, item.message?.id))
  const placed: (T | TodoMarker)[] = [...items]
  placed.splice(index === -1 ? placed.length : index, 0, {
    id: `todo:${todo.conversationId}`,
    todo: true,
  })
  return placed
}
