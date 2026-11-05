import type { DashboardContent, DashboardWidgetContent } from '@ariadne/core'

/**
 * The `props` each widget type of `@dou-ui/dashboard` reads, checked before a dashboard is
 * saved: the widgets read them as they are, so a prop of the wrong type breaks the widget
 * when it is shown. Kept to the types in `@dou-ui/dashboard`'s `widgets/types.ts`,
 * `chart/types.ts` and `pivot/types.ts`.
 */

type Check = (value: unknown) => string | null

const isObject = (value: unknown): value is Record<string, unknown> =>
  typeof value === 'object' && value !== null && !Array.isArray(value)

function got(value: unknown): string {
  if (value === null) return 'null'
  if (Array.isArray(value)) return 'an array'
  if (typeof value === 'string') return `the text ${JSON.stringify(value)}`
  if (typeof value === 'number' || typeof value === 'boolean') {
    return `the ${typeof value} ${String(value)}`
  }
  return `a ${typeof value}`
}

const text: Check = (value) => (typeof value === 'string' ? null : `text, not ${got(value)}`)
const path: Check = (value) =>
  typeof value === 'string' ? null : `a field name or dotted path in the data, not ${got(value)}`
const number: Check = (value) =>
  typeof value === 'number' && Number.isFinite(value) ? null : `a number, not ${got(value)}`
const flag: Check = (value) =>
  typeof value === 'boolean' ? null : `true or false, not ${got(value)}`
const oneOf =
  (...options: string[]): Check =>
  (value) =>
    options.includes(value as string) ? null : `one of ${options.join(', ')}, not ${got(value)}`
const decimals: Check = (value) =>
  Number.isInteger(value) && (value as number) >= 0 && (value as number) <= 20
    ? null
    : `a whole number from 0 to 20, not ${got(value)}`
const paths: Check = (value) =>
  Array.isArray(value) && value.every((v) => typeof v === 'string')
    ? null
    : `a list of field names, not ${got(value)}`
const names: Check = (value) =>
  isObject(value) && Object.values(value).every((v) => typeof v === 'string')
    ? null
    : `an object of field → name, not ${got(value)}`
const columns: Check = (value) => {
  if (!Array.isArray(value)) return `a list of { key, title? }, not ${got(value)}`
  const bad = value.findIndex(
    (c) =>
      !isObject(c) ||
      typeof c.key !== 'string' ||
      (c.title !== undefined && typeof c.title !== 'string'),
  )
  return bad < 0 ? null : `a list of { key, title? }; item ${bad} is not`
}

const AGGREGATES = ['sum', 'count', 'avg', 'min', 'max', 'distinct', 'median']
const dimensions = (value: unknown): boolean =>
  Array.isArray(value) &&
  value.every((d) => typeof d === 'string' || (isObject(d) && typeof d.field === 'string'))
const pivotConfig: Check = (value) => {
  if (!isObject(value)) return `{ rows, columns, values }, not ${got(value)}`
  for (const axis of ['rows', 'columns'] as const) {
    if (value[axis] !== undefined && !dimensions(value[axis])) {
      return `${axis} must be a list of field names`
    }
  }
  const values = value.values
  if (values === undefined) return null
  if (!Array.isArray(values)) return `values must be a list, not ${got(values)}`
  for (const v of values) {
    if (typeof v === 'string') continue
    if (!isObject(v)) return `each of values is a field name or { field, aggregate, label? }`
    if (v.field !== undefined && typeof v.field !== 'string') return "a value's field must be text"
    if (v.aggregate !== undefined && !AGGREGATES.includes(v.aggregate as string)) {
      return `a value's aggregate is one of ${AGGREGATES.join(', ')}, not ${got(v.aggregate)}`
    }
    if (v.label !== undefined && typeof v.label !== 'string') return "a value's label must be text"
  }
  return null
}

const SCHEMAS: Record<string, Record<string, Check>> = {
  stat: {
    source: text,
    field: path,
    value: number,
    prefix: text,
    suffix: text,
    precision: decimals,
    trend: path,
    delta: path,
  },
  text: { text, size: oneOf('sm', 'md', 'lg') },
  table: { source: text, path, columns, rowKey: path },
  chart: {
    source: text,
    path,
    kind: oneOf('line', 'area', 'bar', 'pie'),
    x: path,
    y: paths,
    labels: names,
    stacked: flag,
    horizontal: flag,
    filter: text,
  },
  pivot: { source: text, path, config: pivotConfig, filter: flag },
}

/** What makes a widget's props unreadable for its type; nothing when they are fine. */
export function widgetPropsErrors(widget: DashboardWidgetContent): string[] {
  const schema = SCHEMAS[widget.type]
  if (!schema) return []
  const errors: string[] = []
  for (const [key, value] of Object.entries(widget.props)) {
    const check = schema[key]
    if (!check) {
      errors.push(
        `widget "${widget.id}": a ${widget.type} has no prop "${key}"; it takes ${Object.keys(schema).join(', ')}`,
      )
      continue
    }
    if (value === undefined) continue
    const problem = check(value)
    if (problem) errors.push(`widget "${widget.id}": props.${key} must be ${problem}`)
  }
  const props = widget.props
  if (widget.type === 'stat') {
    if (props.field === undefined && props.value === undefined) {
      errors.push(`widget "${widget.id}": a stat needs value, or source and field`)
    }
    for (const key of ['field', 'trend', 'delta'] as const) {
      if (props[key] !== undefined && props.source === undefined) {
        errors.push(`widget "${widget.id}": props.${key} is read from props.source; give source`)
      }
    }
  }
  if (widget.type === 'chart' || widget.type === 'table' || widget.type === 'pivot') {
    if (props.source === undefined)
      errors.push(`widget "${widget.id}": a ${widget.type} needs props.source`)
  }
  if (widget.type === 'chart') {
    if (props.x === undefined) errors.push(`widget "${widget.id}": a chart needs props.x`)
    if (props.y === undefined) errors.push(`widget "${widget.id}": a chart needs props.y`)
  }
  return errors
}

/** The value at a dotted path, as the widgets read it. */
function valueAt(data: unknown, at: string | undefined): unknown {
  if (!at) return data
  let value = data
  for (const key of at.split('.')) {
    if (!isObject(value)) return undefined
    value = value[key]
  }
  return value
}

/**
 * What a dashboard will show wrong though it can be saved: widgets reading data sets,
 * fields or rows that are not there, which show empty. Nothing when it looks right.
 */
export function dashboardProblems(content: DashboardContent): string[] {
  const problems: string[] = []
  for (const widget of content.dashboard.widgets) {
    const errors = widgetPropsErrors(widget)
    if (errors.length) {
      problems.push(...errors)
      continue
    }
    const props = widget.props as Record<string, string | string[] | undefined>
    const name = props.source as string | undefined
    if (name === undefined) continue
    if (!(name in content.data)) {
      problems.push(`widget "${widget.id}" reads data set "${name}", which is not in data`)
      continue
    }
    const data = content.data[name]
    const at = (label: string) => `widget "${widget.id}": ${label}`
    if (widget.type === 'stat') {
      for (const key of ['field', 'delta'] as const) {
        const field = props[key] as string | undefined
        if (field !== undefined && typeof valueAt(data, field) !== 'number') {
          problems.push(at(`props.${key} "${field}" is not a number in data set "${name}"`))
        }
      }
      const trend = props.trend as string | undefined
      if (trend !== undefined && !Array.isArray(valueAt(data, trend))) {
        problems.push(at(`props.trend "${trend}" is not a list of numbers in data set "${name}"`))
      }
      continue
    }
    if (widget.type === 'text') continue
    const rows = valueAt(data, props.path as string | undefined)
    if (!Array.isArray(rows)) {
      const where = props.path
        ? `"${String(props.path)}" in data set "${name}"`
        : `data set "${name}"`
      problems.push(at(`${where} is not a list of rows`))
      continue
    }
    const first = rows[0]
    if (!isObject(first)) continue
    const missing = (key: string) => !((key.split('.')[0] ?? key) in first)
    const keys: string[] = []
    if (widget.type === 'chart') keys.push(props.x as string, ...(props.y as string[]))
    if (widget.type === 'table') {
      for (const column of (widget.props.columns ?? []) as { key: string }[]) keys.push(column.key)
    }
    const gone = keys.filter(missing)
    if (gone.length) {
      problems.push(
        at(`rows of data set "${name}" have no ${gone.map((k) => `"${k}"`).join(', ')}`),
      )
    }
  }
  return problems
}
