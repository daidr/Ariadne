import { DomainError } from './errors'

/**
 * Interface blocks: structured pieces an agent puts in a message, each drawn by a dou-ui
 * component. The set is fixed; every block is checked here before it is stored, so the
 * web can hand a stored block to its component as it is. Nothing the model writes is ever
 * run or rendered as markup.
 *
 * Choices, confirmations and forms ask the user something (ask blocks): each is an
 * attention, listed under "needs me" until answered, and the answer wakes whoever asked.
 * Tables, charts and steps only show something.
 */

export interface ChoiceOption {
  label: string
  /** What happens if it is picked, in one line. */
  consequence: string
}

export interface ChoiceBlock {
  type: 'choice'
  question: string
  options: ChoiceOption[]
  /** The option the asker recommends. */
  recommended: number | null
}

export type ConfirmTone = 'neutral' | 'warning' | 'danger'

export interface ConfirmBlock {
  type: 'confirm'
  /** The question, e.g. "Push to the public repository?". */
  title: string
  /** What agreeing does, in a sentence. */
  description: string | null
  /** Shown in monospace under it, e.g. the command or the text that will be sent. */
  detail: string | null
  tone: ConfirmTone
  approveText: string | null
  denyText: string | null
}

export const FORM_FIELD_KINDS = [
  'text',
  'textarea',
  'number',
  'select',
  'multiselect',
  'switch',
] as const
export type FormFieldKind = (typeof FORM_FIELD_KINDS)[number]
export type FormValue = string | number | boolean | string[] | null

export interface FormField {
  /** The key of the value in the answer. */
  key: string
  label: string
  kind: FormFieldKind
  required: boolean
  placeholder: string | null
  /** The choices of a select or multiselect. */
  options: { label: string; value: string }[]
  /** The value it starts with; always a valid value of the field. */
  default: FormValue
  /** Bounds of a number. */
  min: number | null
  max: number | null
}

export interface FormBlock {
  type: 'form'
  title: string
  description: string | null
  fields: FormField[]
  submitText: string | null
}

export type TableCell = string | number | boolean | null

export interface TableColumn {
  key: string
  title: string
  /** `end` for numbers, so the digits line up. */
  align: 'start' | 'end'
}

export interface TableBlock {
  type: 'table'
  title: string | null
  columns: TableColumn[]
  rows: Record<string, TableCell>[]
}

export const CHART_KINDS = ['line', 'area', 'bar', 'pie'] as const
export type ChartKind = (typeof CHART_KINDS)[number]

export interface ChartBlock {
  type: 'chart'
  title: string | null
  kind: ChartKind
  /** The key of each row along x: categories, numbers or dates; a pie's slice names. */
  x: string
  /** The keys drawn, one series each; a pie has exactly one. */
  y: string[]
  /** Names of the y keys in the legend and tooltip. */
  labels: Record<string, string>
  rows: Record<string, string | number | null>[]
  stacked: boolean
  horizontal: boolean
}

export const STEP_STATUSES = ['done', 'current', 'pending', 'failed'] as const
export type StepStatus = (typeof STEP_STATUSES)[number]

export interface StepsBlock {
  type: 'steps'
  title: string | null
  items: { title: string; description: string | null; status: StepStatus }[]
}

export type AskBlock = ChoiceBlock | ConfirmBlock | FormBlock
export type ShowBlock = TableBlock | ChartBlock | StepsBlock
export type UiBlock = AskBlock | ShowBlock

export const ASK_BLOCK_TYPES = ['choice', 'confirm', 'form'] as const
export const SHOW_BLOCK_TYPES = ['table', 'chart', 'steps'] as const
export const BLOCK_TYPES = [...ASK_BLOCK_TYPES, ...SHOW_BLOCK_TYPES] as const

/** How the user answered an ask block; its `type` is the block's. */
export type BlockAnswer =
  | { type: 'choice'; choice: number }
  | { type: 'confirm'; approved: boolean }
  | { type: 'form'; values: Record<string, FormValue> }

export const isAskBlock = (block: UiBlock): block is AskBlock =>
  (ASK_BLOCK_TYPES as readonly string[]).includes(block.type)

/** Limits that keep a message readable and its event small. */
export const BLOCK_LIMITS = {
  text: 2000,
  label: 120,
  options: 6,
  fields: 20,
  fieldOptions: 50,
  columns: 12,
  rows: 200,
  chartRows: 500,
  series: 8,
  steps: 20,
  answerText: 10_000,
  blocks: 6,
} as const

function invalid(reason: string): never {
  throw new DomainError('error.blockInvalid', { reason })
}

const isObject = (value: unknown): value is Record<string, unknown> =>
  typeof value === 'object' && value !== null && !Array.isArray(value)

const isNumber = (value: unknown): value is number =>
  typeof value === 'number' && Number.isFinite(value)

/** A value the object itself holds, never one from its prototype (`toString` as a key). */
const own = (value: Record<string, unknown>, name: string): unknown =>
  Object.hasOwn(value, name) ? value[name] : undefined

const KEY = /^[A-Za-z_][\w-]{0,39}$/

function text(value: unknown, at: string, max: number = BLOCK_LIMITS.text): string {
  if (typeof value !== 'string' || !value.trim()) invalid(`${at} must be a non-empty string`)
  if (value.length > max) invalid(`${at} is longer than ${max} characters`)
  return value.trim()
}

function optionalText(value: unknown, at: string, max: number = BLOCK_LIMITS.text): string | null {
  if (value === undefined || value === null || value === '') return null
  return text(value, at, max)
}

function optionalBool(value: unknown, at: string): boolean {
  if (value === undefined || value === null) return false
  if (typeof value !== 'boolean') invalid(`${at} must be true or false`)
  return value
}

function optionalNumber(value: unknown, at: string): number | null {
  if (value === undefined || value === null) return null
  if (!isNumber(value)) invalid(`${at} must be a number`)
  return value
}

function list(value: unknown, at: string, min: number, max: number): unknown[] {
  if (!Array.isArray(value)) invalid(`${at} must be an array`)
  if (value.length < min) invalid(`${at} needs at least ${min} item${min === 1 ? '' : 's'}`)
  if (value.length > max) invalid(`${at} has more than ${max} items`)
  return value
}

function key(value: unknown, at: string): string {
  if (typeof value !== 'string' || !KEY.test(value)) {
    invalid(`${at} must be a short key of letters, digits, _ or -`)
  }
  return value
}

function oneOf<T extends string>(
  value: unknown,
  allowed: readonly T[],
  at: string,
  fallback?: T,
): T {
  if ((value === undefined || value === null) && fallback !== undefined) return fallback
  if (!(allowed as readonly unknown[]).includes(value)) {
    invalid(`${at} must be one of ${allowed.join(', ')}`)
  }
  return value as T
}

function only(value: Record<string, unknown>, allowed: string[], at: string): void {
  const extra = Object.keys(value).filter((name) => !allowed.includes(name))
  if (extra.length) invalid(`${at} has unknown fields: ${extra.join(', ')}`)
}

function parseChoice(value: Record<string, unknown>, at: string): ChoiceBlock {
  only(value, ['type', 'question', 'options', 'recommended'], at)
  const options = list(value.options, `${at}.options`, 2, BLOCK_LIMITS.options).map((raw, i) => {
    const where = `${at}.options[${i}]`
    if (!isObject(raw)) invalid(`${where} must be { label, consequence }`)
    only(raw, ['label', 'consequence'], where)
    return {
      label: text(raw.label, `${where}.label`, BLOCK_LIMITS.label),
      consequence: text(raw.consequence, `${where}.consequence`),
    }
  })
  const recommended = optionalNumber(value.recommended, `${at}.recommended`)
  if (recommended !== null && (!Number.isInteger(recommended) || !options[recommended])) {
    invalid(`${at}.recommended must be the index of an option`)
  }
  return { type: 'choice', question: text(value.question, `${at}.question`), options, recommended }
}

function parseConfirm(value: Record<string, unknown>, at: string): ConfirmBlock {
  only(value, ['type', 'title', 'description', 'detail', 'tone', 'approveText', 'denyText'], at)
  return {
    type: 'confirm',
    title: text(value.title, `${at}.title`),
    description: optionalText(value.description, `${at}.description`),
    detail: optionalText(value.detail, `${at}.detail`, BLOCK_LIMITS.answerText),
    tone: oneOf(value.tone, ['neutral', 'warning', 'danger'], `${at}.tone`, 'neutral'),
    approveText: optionalText(value.approveText, `${at}.approveText`, BLOCK_LIMITS.label),
    denyText: optionalText(value.denyText, `${at}.denyText`, BLOCK_LIMITS.label),
  }
}

function parseField(raw: unknown, at: string): FormField {
  if (!isObject(raw)) invalid(`${at} must be an object`)
  only(
    raw,
    ['key', 'label', 'kind', 'required', 'placeholder', 'options', 'default', 'min', 'max'],
    at,
  )
  const kind = oneOf(raw.kind, FORM_FIELD_KINDS, `${at}.kind`)
  const choosing = kind === 'select' || kind === 'multiselect'
  const options = choosing
    ? list(raw.options, `${at}.options`, 1, BLOCK_LIMITS.fieldOptions).map((option, i) => {
        const where = `${at}.options[${i}]`
        if (typeof option === 'string') {
          return { label: text(option, where, BLOCK_LIMITS.label), value: option }
        }
        if (!isObject(option)) invalid(`${where} must be a string or { label, value }`)
        only(option, ['label', 'value'], where)
        return {
          label: text(option.label, `${where}.label`, BLOCK_LIMITS.label),
          value: text(option.value, `${where}.value`, BLOCK_LIMITS.label),
        }
      })
    : []
  // A checked block comes back with `options: []`, so it checks again the same.
  if (!choosing && raw.options != null && !(Array.isArray(raw.options) && !raw.options.length)) {
    invalid(`${at}.options is only for select fields`)
  }
  if (new Set(options.map((option) => option.value)).size !== options.length) {
    invalid(`${at}.options has the same value twice`)
  }
  const bounded = kind === 'number'
  if (!bounded && (raw.min != null || raw.max != null)) {
    invalid(`${at}.min and max are only for number fields`)
  }
  const field: FormField = {
    key: key(raw.key, `${at}.key`),
    label: text(raw.label, `${at}.label`, BLOCK_LIMITS.label),
    kind,
    required: kind === 'switch' ? false : optionalBool(raw.required, `${at}.required`),
    placeholder: optionalText(raw.placeholder, `${at}.placeholder`, BLOCK_LIMITS.label),
    options,
    default: null,
    min: bounded ? optionalNumber(raw.min, `${at}.min`) : null,
    max: bounded ? optionalNumber(raw.max, `${at}.max`) : null,
  }
  if (field.min !== null && field.max !== null && field.min > field.max) {
    invalid(`${at}.min is greater than max`)
  }
  field.default =
    raw.default === undefined || raw.default === null
      ? emptyValue(field)
      : fieldValue(field, raw.default, `${at}.default`, false)
  return field
}

function parseForm(value: Record<string, unknown>, at: string): FormBlock {
  only(value, ['type', 'title', 'description', 'fields', 'submitText'], at)
  const fields = list(value.fields, `${at}.fields`, 1, BLOCK_LIMITS.fields).map((raw, i) =>
    parseField(raw, `${at}.fields[${i}]`),
  )
  const keys = new Set<string>()
  for (const field of fields) {
    if (keys.has(field.key)) invalid(`${at}.fields uses the key "${field.key}" twice`)
    keys.add(field.key)
  }
  return {
    type: 'form',
    title: text(value.title, `${at}.title`),
    description: optionalText(value.description, `${at}.description`),
    fields,
    submitText: optionalText(value.submitText, `${at}.submitText`, BLOCK_LIMITS.label),
  }
}

function parseTable(value: Record<string, unknown>, at: string): TableBlock {
  only(value, ['type', 'title', 'columns', 'rows'], at)
  const columns = list(value.columns, `${at}.columns`, 1, BLOCK_LIMITS.columns).map((raw, i) => {
    const where = `${at}.columns[${i}]`
    if (!isObject(raw)) invalid(`${where} must be { key, title }`)
    only(raw, ['key', 'title', 'align'], where)
    return {
      key: key(raw.key, `${where}.key`),
      title: text(raw.title, `${where}.title`, BLOCK_LIMITS.label),
      align: oneOf(raw.align, ['start', 'end'], `${where}.align`, 'start'),
    }
  })
  const keys = columns.map((column) => column.key)
  if (new Set(keys).size !== keys.length) invalid(`${at}.columns has the same key twice`)
  const rows = list(value.rows, `${at}.rows`, 0, BLOCK_LIMITS.rows).map((raw, i) => {
    const where = `${at}.rows[${i}]`
    if (!isObject(raw)) invalid(`${where} must be an object of cells by column key`)
    only(raw, keys, where)
    return Object.fromEntries(
      keys.map((name) => {
        const cell = own(raw, name) ?? null
        if (cell !== null && typeof cell !== 'string' && typeof cell !== 'boolean') {
          if (!isNumber(cell)) invalid(`${where}.${name} must be text, a number or a boolean`)
        }
        if (typeof cell === 'string' && cell.length > BLOCK_LIMITS.text) {
          invalid(`${where}.${name} is longer than ${BLOCK_LIMITS.text} characters`)
        }
        return [name, cell as TableCell]
      }),
    )
  })
  return { type: 'table', title: optionalText(value.title, `${at}.title`), columns, rows }
}

function parseChart(value: Record<string, unknown>, at: string): ChartBlock {
  only(value, ['type', 'title', 'kind', 'x', 'y', 'labels', 'rows', 'stacked', 'horizontal'], at)
  const kind = oneOf(value.kind, CHART_KINDS, `${at}.kind`)
  const x = key(value.x, `${at}.x`)
  const y = list(value.y, `${at}.y`, 1, kind === 'pie' ? 1 : BLOCK_LIMITS.series).map((raw, i) =>
    key(raw, `${at}.y[${i}]`),
  )
  if (new Set(y).size !== y.length) invalid(`${at}.y names the same key twice`)
  if (y.includes(x)) invalid(`${at}.y must not include x`)
  let labels: Record<string, string> = {}
  if (value.labels !== undefined && value.labels !== null) {
    if (!isObject(value.labels)) invalid(`${at}.labels must be an object of names by y key`)
    labels = Object.fromEntries(
      Object.entries(value.labels).map(([name, label]) => {
        if (!y.includes(name)) invalid(`${at}.labels.${name} is not one of y`)
        return [name, text(label, `${at}.labels.${name}`, BLOCK_LIMITS.label)]
      }),
    )
  }
  const rows = list(value.rows, `${at}.rows`, 1, BLOCK_LIMITS.chartRows).map((raw, i) => {
    const where = `${at}.rows[${i}]`
    if (!isObject(raw)) invalid(`${where} must be an object`)
    only(raw, [x, ...y], where)
    const xValue = own(raw, x)
    if (typeof xValue !== 'string' && !isNumber(xValue))
      invalid(`${where}.${x} must be text or a number`)
    if (typeof xValue === 'string' && xValue.length > BLOCK_LIMITS.label) {
      invalid(`${where}.${x} is longer than ${BLOCK_LIMITS.label} characters`)
    }
    const row: Record<string, string | number | null> = { [x]: xValue }
    for (const name of y) {
      const v = own(raw, name) ?? null
      if (v !== null && !isNumber(v)) invalid(`${where}.${name} must be a number or null`)
      row[name] = v
    }
    return row
  })
  return {
    type: 'chart',
    title: optionalText(value.title, `${at}.title`),
    kind,
    x,
    y,
    labels,
    rows,
    stacked: kind === 'pie' ? false : optionalBool(value.stacked, `${at}.stacked`),
    horizontal: kind === 'bar' ? optionalBool(value.horizontal, `${at}.horizontal`) : false,
  }
}

function parseSteps(value: Record<string, unknown>, at: string): StepsBlock {
  only(value, ['type', 'title', 'items'], at)
  const items = list(value.items, `${at}.items`, 1, BLOCK_LIMITS.steps).map((raw, i) => {
    const where = `${at}.items[${i}]`
    if (!isObject(raw)) invalid(`${where} must be { title, status }`)
    only(raw, ['title', 'description', 'status'], where)
    return {
      title: text(raw.title, `${where}.title`, BLOCK_LIMITS.label),
      description: optionalText(raw.description, `${where}.description`),
      status: oneOf(raw.status, STEP_STATUSES, `${where}.status`, 'pending'),
    }
  })
  return { type: 'steps', title: optionalText(value.title, `${at}.title`), items }
}

/**
 * Check a block as an agent wrote it and fill in what it left out. Throws `blockInvalid`
 * with a reason the model can act on.
 */
export function parseBlock(value: unknown, at = 'block'): UiBlock {
  if (!isObject(value)) invalid(`${at} must be an object`)
  const type = oneOf(value.type, BLOCK_TYPES, `${at}.type`)
  switch (type) {
    case 'choice':
      return parseChoice(value, at)
    case 'confirm':
      return parseConfirm(value, at)
    case 'form':
      return parseForm(value, at)
    case 'table':
      return parseTable(value, at)
    case 'chart':
      return parseChart(value, at)
    case 'steps':
      return parseSteps(value, at)
  }
}

/** The value a field holds when nothing was given. */
function emptyValue(field: FormField): FormValue {
  if (field.kind === 'switch') return false
  if (field.kind === 'multiselect') return []
  return null
}

/**
 * Check one field's value. `strict` also enforces `required`, which a default need not
 * meet (the user fills it in).
 */
function fieldValue(field: FormField, value: unknown, at: string, strict: boolean): FormValue {
  const missing = () => {
    if (strict && field.required) invalid(`${at} is required`)
    return emptyValue(field)
  }
  if (value === undefined || value === null) return missing()
  switch (field.kind) {
    case 'text':
    case 'textarea': {
      if (typeof value !== 'string') invalid(`${at} must be text`)
      if (value.length > BLOCK_LIMITS.answerText) {
        invalid(`${at} is longer than ${BLOCK_LIMITS.answerText} characters`)
      }
      return value.trim() ? value : missing()
    }
    case 'number': {
      if (!isNumber(value)) invalid(`${at} must be a number`)
      if (field.min !== null && value < field.min) invalid(`${at} must be at least ${field.min}`)
      if (field.max !== null && value > field.max) invalid(`${at} must be at most ${field.max}`)
      return value
    }
    case 'select': {
      if (!field.options.some((option) => option.value === value)) {
        invalid(`${at} must be one of the field's option values`)
      }
      return value as string
    }
    case 'multiselect': {
      if (!Array.isArray(value)) invalid(`${at} must be a list of option values`)
      const values = new Set(field.options.map((option) => option.value))
      if (!value.every((v) => typeof v === 'string' && values.has(v))) {
        invalid(`${at} must hold only the field's option values`)
      }
      if (new Set(value).size !== value.length) invalid(`${at} names a value twice`)
      return value.length ? (value as string[]) : missing()
    }
    case 'switch':
      if (typeof value !== 'boolean') invalid(`${at} must be true or false`)
      return value
  }
}

/** Check an answer against the block it answers. */
export function parseAnswer(block: AskBlock, value: unknown): BlockAnswer {
  if (!isObject(value) || value.type !== block.type) invalid(`the answer must be a ${block.type}`)
  switch (block.type) {
    case 'choice': {
      const choice = value.choice
      if (typeof choice !== 'number' || !Number.isInteger(choice) || !block.options[choice]) {
        invalid('choice must be the index of an option')
      }
      return { type: 'choice', choice }
    }
    case 'confirm':
      if (typeof value.approved !== 'boolean') invalid('approved must be true or false')
      return { type: 'confirm', approved: value.approved }
    case 'form': {
      const given = value.values
      if (!isObject(given)) invalid('values must be an object by field key')
      only(
        given,
        block.fields.map((field) => field.key),
        'values',
      )
      return {
        type: 'form',
        values: Object.fromEntries(
          block.fields.map((field) => [
            field.key,
            fieldValue(field, own(given, field.key), `values.${field.key}`, true),
          ]),
        ),
      }
    }
  }
}

/** What an ask block asks, in one line: its question or title. */
export const askTitle = (block: AskBlock): string =>
  block.type === 'choice' ? block.question : block.title

/** An answer as the asker's model reads it. */
export function describeAnswer(block: AskBlock, answer: BlockAnswer): string {
  if (block.type === 'choice' && answer.type === 'choice') {
    return `choice: ${block.options[answer.choice]?.label ?? answer.choice}`
  }
  if (answer.type === 'confirm') return answer.approved ? 'approved' : 'declined'
  if (block.type === 'form' && answer.type === 'form') {
    const lines = block.fields.map(
      (field) =>
        `${field.key} (${field.label}): ${JSON.stringify(answer.values[field.key] ?? null)}`,
    )
    return `submitted:\n${lines.join('\n')}`
  }
  return JSON.stringify(answer)
}

/** A confirmation with what was not given left out, for cards the system builds. */
export function confirmBlock(
  input: Pick<ConfirmBlock, 'title'> & Partial<Omit<ConfirmBlock, 'type' | 'title'>>,
): ConfirmBlock {
  return {
    type: 'confirm',
    title: input.title,
    description: input.description ?? null,
    detail: input.detail ?? null,
    tone: input.tone ?? 'neutral',
    approveText: input.approveText ?? null,
    denyText: input.denyText ?? null,
  }
}
