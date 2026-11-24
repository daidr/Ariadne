export interface AskBlockOption {
  id: string
  label: string
  note?: string
  recommended?: boolean
}

export interface AskBlockProps {
  id: string
  question: string
  description: string
  pick: 'one' | 'many' | 'text'
  options: AskBlockOption[]
  allowNote: boolean
  selected: string[]
  note: string
  confirmed: boolean
}

export const DEFAULT_ASK_PROPS: AskBlockProps = {
  id: '',
  question: '',
  description: '',
  pick: 'one',
  options: [],
  allowNote: true,
  selected: [],
  note: '',
  confirmed: false,
}

export interface RefBlockProps {
  artifactId: string
  groupId: string
  optionId: string
  label: string
}

export const DEFAULT_REF_PROPS: RefBlockProps = {
  artifactId: '',
  groupId: '',
  optionId: '',
  label: '',
}

/** Deterministic short fallback ID when an inline <ask> omits id. */
function fallbackAskId(question: string): string {
  let hash = 2166136261
  for (let i = 0; i < question.length; i++) {
    hash ^= question.charCodeAt(i)
    hash = Math.imul(hash, 16777619)
  }
  return `ask-${(hash >>> 0).toString(16).padStart(8, '0')}`
}

export function normalizeAskProps(raw: Record<string, unknown>): AskBlockProps | null {
  const str = (key: string) => (typeof raw[key] === 'string' ? raw[key].trim() : '')
  const question = str('question')
  if (!question) return null
  const id = str('id') || fallbackAskId(question)
  const options: AskBlockOption[] = []
  const seenOpt = new Set<string>()
  if (Array.isArray(raw.options)) {
    for (let i = 0; i < raw.options.length; i++) {
      const item = raw.options[i]
      let optId = ''
      let label = ''
      let note = ''
      let recommended = false
      if (typeof item === 'string') {
        label = item.trim()
      } else if (item && typeof item === 'object') {
        const record = item as Record<string, unknown>
        if (typeof record.id === 'string') optId = record.id.trim()
        if (typeof record.label === 'string') label = record.label.trim()
        if (typeof record.note === 'string') note = record.note.trim()
        recommended = record.recommended === true
      }
      if (!label) continue
      if (!optId) optId = `opt-${i + 1}`
      if (seenOpt.has(optId)) continue
      seenOpt.add(optId)
      const entry: AskBlockOption = { id: optId, label }
      if (note) entry.note = note
      if (recommended) entry.recommended = true
      options.push(entry)
    }
  }
  const rawPick = str('pick')
  let pick: 'one' | 'many' | 'text' =
    rawPick === 'one' || rawPick === 'many' || rawPick === 'text'
      ? rawPick
      : options.length > 0
        ? 'one'
        : 'text'
  if (options.length === 0) pick = 'text'
  const allowNote = typeof raw.allowNote === 'boolean' ? raw.allowNote : true
  const selected: string[] = []
  const seenSel = new Set<string>()
  if (Array.isArray(raw.selected)) {
    for (const item of raw.selected) {
      if (typeof item !== 'string') continue
      const s = item.trim()
      if (!s || seenSel.has(s)) continue
      seenSel.add(s)
      selected.push(s)
    }
  }
  const note = typeof raw.note === 'string' ? raw.note : ''
  const confirmed = raw.confirmed === true
  return {
    id,
    question,
    description: str('description'),
    pick,
    options,
    allowNote,
    selected,
    note,
    confirmed,
  }
}

export function serializeAskPayload(raw: Record<string, unknown>): Record<string, unknown> | null {
  const normalized = normalizeAskProps(raw)
  if (!normalized) return null
  const out: Record<string, unknown> = {
    id: normalized.id,
    question: normalized.question,
    pick: normalized.pick,
  }
  if (normalized.description) out.description = normalized.description
  if (normalized.options.length > 0) out.options = normalized.options
  if (!normalized.allowNote) out.allowNote = false
  if (normalized.selected.length > 0) out.selected = normalized.selected
  if (normalized.note) out.note = normalized.note
  if (normalized.confirmed) out.confirmed = true
  return out
}

export function normalizeRefProps(raw: Record<string, unknown>): RefBlockProps | null {
  const str = (...keys: string[]) => {
    for (const key of keys) {
      const value = raw[key]
      if (typeof value === 'string' && value.trim()) return value.trim()
    }
    return ''
  }
  const artifactId = str('artifactId', 'artifact').replace(/^artifact:/, '')
  if (!artifactId) return null
  return {
    artifactId,
    groupId: str('groupId', 'group'),
    optionId: str('optionId', 'option'),
    label: str('label'),
  }
}

export function serializeRefPayload(raw: Record<string, unknown>): Record<string, unknown> | null {
  const normalized = normalizeRefProps(raw)
  if (!normalized) return null
  const out: Record<string, unknown> = { artifactId: normalized.artifactId }
  if (normalized.groupId) out.groupId = normalized.groupId
  if (normalized.optionId) out.optionId = normalized.optionId
  if (normalized.label) out.label = normalized.label
  return out
}
