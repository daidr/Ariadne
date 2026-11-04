import {
  DASHBOARD_WIDGET_TYPES,
  type DashboardContent,
  type DashboardWidgetContent,
} from '@ariadne/core'
import { ToolInputError } from './tool-helpers'

/** Columns on the widest breakpoint, where `@dou-ui/dashboard` keeps the main place. */
const COLUMNS = 12
const MAIN = 'lg'

const SIZE: Record<string, { w: number; h: number }> = {
  stat: { w: 3, h: 2 },
  text: { w: 6, h: 2 },
  table: { w: 6, h: 4 },
  chart: { w: 6, h: 4 },
  pivot: { w: 12, h: 5 },
}

export interface DashboardChanges {
  widgets: Record<string, unknown>[]
  removeWidgets: string[]
  data?: Record<string, unknown>
  removeData: string[]
}

function whole(value: unknown, key: string, min: number, max: number): number | undefined {
  if (value === undefined || value === null) return undefined
  if (!Number.isInteger(value)) throw new ToolInputError(`"${key}" must be a whole number`)
  return Math.min(max, Math.max(min, value as number))
}

/** Lay widgets out in order, row by row, below `top`. */
function pack(
  widgets: { widget: DashboardWidgetContent; w: number; h: number }[],
  top: number,
): void {
  let x = 0
  let y = top
  let row = 0
  for (const { widget, w, h } of widgets) {
    if (x + w > COLUMNS) {
      x = 0
      y += row
      row = 0
    }
    widget.place = { [MAIN]: { x, y, w, h } }
    x += w
    row = Math.max(row, h)
  }
}

/** Apply the model's changes to a dashboard; from an empty one, this builds a new one. */
export function mergeDashboard(
  content: DashboardContent,
  changes: DashboardChanges,
  now: number,
): DashboardContent {
  const removed = new Set(changes.removeWidgets)
  const widgets = content.dashboard.widgets.filter((w) => !removed.has(w.id))
  const added: { widget: DashboardWidgetContent; w: number; h: number }[] = []
  const seen = new Set<string>()
  for (const input of changes.widgets) {
    const id = input.id
    if (typeof id !== 'string' || !id) throw new ToolInputError('every widget needs an "id"')
    if (seen.has(id)) throw new ToolInputError(`widget id "${id}" is given twice`)
    seen.add(id)
    const existing = widgets.find((w) => w.id === id)
    const type = input.type ?? existing?.type
    if (!(DASHBOARD_WIDGET_TYPES as readonly unknown[]).includes(type)) {
      throw new ToolInputError(`widget "${id}" needs a type: ${DASHBOARD_WIDGET_TYPES.join(', ')}`)
    }
    if (existing && type !== existing.type) {
      throw new ToolInputError(`widget "${id}" is a ${existing.type}; remove it and add another`)
    }
    const props = input.props ?? existing?.props ?? {}
    if (typeof props !== 'object' || Array.isArray(props) || props === null) {
      throw new ToolInputError(`widget "${id}" props must be an object`)
    }
    const widget: DashboardWidgetContent = {
      ...existing,
      id,
      type: type as string,
      props: props as Record<string, unknown>,
    }
    if (typeof input.title === 'string') {
      if (input.title) widget.title = input.title
      else delete widget.title
    }
    const size = SIZE[type as string] ?? { w: 4, h: 3 }
    const w = whole(input.w, 'w', 1, COLUMNS)
    const h = whole(input.h, 'h', 1, 40)
    if (existing) {
      const main = existing.place?.[MAIN]
      if (main && (w !== undefined || h !== undefined)) {
        // Only the main place is kept: narrower breakpoints are worked out from it again.
        widget.place = { [MAIN]: { ...main, w: w ?? main.w, h: h ?? main.h } }
      }
      widgets[widgets.indexOf(existing)] = widget
    } else {
      widgets.push(widget)
      added.push({ widget, w: w ?? size.w, h: h ?? size.h })
    }
  }
  const bottom = Math.max(
    0,
    ...widgets
      .filter((w) => !added.some((a) => a.widget === w))
      .map((w) => {
        const main = w.place?.[MAIN]
        return main ? main.y + main.h : 0
      }),
  )
  pack(added, bottom)

  const data = { ...content.data }
  for (const key of changes.removeData) delete data[key]
  Object.assign(data, changes.data ?? {})
  const takenAt = changes.data ? now : content.takenAt
  return {
    version: 1,
    dashboard: { version: 1, widgets },
    data,
    ...(takenAt === undefined ? {} : { takenAt }),
    ...(content.source ? { source: content.source } : {}),
  }
}

export function dashboardFromWidgets(
  widgets: Record<string, unknown>[],
  data: Record<string, unknown>,
  now: number,
): DashboardContent {
  return mergeDashboard(
    { version: 1, dashboard: { version: 1, widgets: [] }, data: {} },
    { widgets, removeWidgets: [], data, removeData: [] },
    now,
  )
}
