import { shallowRef } from 'vue'
import type { SheetData, SheetRow } from '@dou-ui/sheet'

export interface PreviewFile {
  id: string
  name?: string
  mime?: string
  size?: number
}

export const previewFile = shallowRef<PreviewFile | null>(null)

export function openFilePreview(file: PreviewFile) {
  previewFile.value = file
}

export function previewKind(mime: string, name: string) {
  const type = mime.split(';', 1)[0]?.trim().toLowerCase()
  if (type === 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet') return 'sheet'
  if (type === 'video/mp4' || type === 'video/webm') return 'video'
  if (/^image\/(png|jpeg|gif|webp|avif)$/.test(type ?? '')) return 'image'
  if (type === 'text/plain') {
    if (/\.csv$/i.test(name)) return 'csv'
    if (/\.tsv$/i.test(name)) return 'tsv'
    return 'text'
  }
}

export function codeLanguage(name: string) {
  const basename = name.split(/[\\/]/).at(-1)?.toLowerCase()
  if (basename?.match(/^dockerfile(?:\.|$)/)) return 'dockerfile'
  if (basename?.match(/^\.env(?:\.|$)/)) return 'ini'
  const extension = basename?.includes('.') ? basename.split('.').at(-1) : undefined
  const aliases: Record<string, string> = {
    cjs: 'javascript',
    mjs: 'javascript',
    md: 'markdown',
    yml: 'yaml',
    env: 'ini',
    conf: 'ini',
  }
  return extension ? (aliases[extension] ?? extension) : undefined
}

export interface SheetPreviewRows {
  rows: string[][]
  rowNumbers?: number[]
  truncated: boolean
}

export type SheetPreviewSource = { fileId: string } | SheetPreviewRows

export function previewSheetData(name: string, rows: string[][], rowNumbers?: number[]): SheetData {
  const cells: SheetRow[] = []
  let colCount = 1
  for (const [index, row] of rows.entries()) {
    const number = rowNumbers?.[index] ?? index + 1
    if (!Number.isInteger(number) || number < 1 || number > 1_048_576) continue
    // Preview attachments as text. A leading "=" has special meaning in DouSheet.
    cells[number - 1] = row.map((value) => (value.startsWith('=') ? `'${value}` : value))
    colCount = Math.max(colCount, row.length)
  }
  return { name, cells, rowCount: Math.max(cells.length, 1), colCount }
}

export function parseDelimited(text: string, delimiter: string) {
  const rows: string[][] = []
  let row: string[] = []
  let cell = ''
  let hasCell = false
  let quoted = false
  let truncated = false

  function endCell() {
    if (row.length < 40) row.push(cell.slice(0, 4096))
    else truncated = true
    if (cell.length > 4096) truncated = true
    cell = ''
    hasCell = false
  }

  for (let i = 0; i < text.length; i++) {
    if (rows.length === 250) {
      truncated = true
      break
    }
    const c = text[i]
    if (c === '"') {
      const atStart = !hasCell
      hasCell = true
      if (quoted && text[i + 1] === '"') {
        cell += '"'
        i++
      } else if (quoted || atStart) quoted = !quoted
      else cell += c
    } else if (!quoted && (c === delimiter || c === '\r' || c === '\n')) {
      endCell()
      if (c !== delimiter) {
        rows.push(row)
        row = []
        if (c === '\r' && text[i + 1] === '\n') i++
      }
    } else {
      cell += c
      hasCell = true
    }
  }
  if (rows.length < 250 && (hasCell || row.length)) {
    endCell()
    rows.push(row)
  }
  return { rows, truncated }
}
