/**
 * Interface blocks: structured pieces an agent puts in a message, each drawn by a dou-ui
 * component. The set is fixed; every block is checked here before it is stored, so the
 * web can hand a stored block to its component as it is. Nothing the model writes is ever
 * run or rendered as markup.
 *
 * Choices, confirmations and forms ask the user something (ask blocks): each is an
 * attention, listed under "needs me" until answered, and the answer wakes whoever asked.
 * Tables, charts, steps and threads only show something.
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
  /** System reference approvals can offer project-wide access. */
  allowProjectReference?: true
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

/** An entry to one of the project's threads, drawn with its stage as it is now. */
export interface ThreadBlock {
  type: 'thread'
  threadId: string
}

/**
 * An external service (an MCP server) an agent suggests the user add: the card opens the
 * settings with it filled in. Secret values are never part of it, only their names.
 */
export interface ConnectorBlock {
  type: 'connector'
  name: string
  /** Why it helps, in a sentence. */
  why: string | null
  transport: 'stdio' | 'http'
  /** stdio: the program and its arguments. */
  command: string | null
  args: string[]
  /** http: where it listens. */
  url: string | null
  /** Names of the environment variables (stdio) or headers (http) the user fills in. */
  secrets: string[]
}

export type AskBlock = ChoiceBlock | ConfirmBlock | FormBlock

export type ShowBlock = TableBlock | ChartBlock | StepsBlock | ThreadBlock | ConnectorBlock

export type UiBlock = AskBlock | ShowBlock

export const ASK_BLOCK_TYPES = ['choice', 'confirm', 'form'] as const

export const SHOW_BLOCK_TYPES = ['table', 'chart', 'steps', 'thread', 'connector'] as const

export const BLOCK_TYPES = [...ASK_BLOCK_TYPES, ...SHOW_BLOCK_TYPES] as const

/** How the user answered an ask block; its `type` is the block's. */
export type BlockAnswer =
  | { type: 'choice'; choice: number }
  | { type: 'confirm'; approved: boolean; scope?: 'thread' | 'project' }
  | { type: 'form'; values: Record<string, FormValue> }
