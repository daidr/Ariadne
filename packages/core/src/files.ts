import type { Database } from 'bun:sqlite'
import { createHash, randomBytes } from 'node:crypto'
import { DomainError } from './errors'

/** The images a whiteboard or a message can hold. SVG is left out: it can carry script. */
export const IMAGE_TYPES = [
  'image/png',
  'image/jpeg',
  'image/gif',
  'image/webp',
  'image/avif',
] as const
export type ImageType = (typeof IMAGE_TYPES)[number]
/** A file is an image, or text attached to a message (code, a log, notes), kept as UTF-8. */
export type FileType = ImageType | 'text/plain'

/** The images both model protocols read; others are described to the model instead. */
export const MODEL_IMAGE_TYPES: readonly string[] = [
  'image/png',
  'image/jpeg',
  'image/gif',
  'image/webp',
]
/** Larger images are refused by model services, so the model is told about them instead. */
export const MAX_MODEL_IMAGE_BYTES = 5 * 1024 * 1024

export const MAX_FILE_BYTES = 10 * 1024 * 1024
/** Text attachments are read whole by the model, so they stay small. */
export const MAX_TEXT_FILE_BYTES = 1024 * 1024

export interface StoredFile {
  id: string
  /** Where the page loads it from. */
  url: string
  mime: FileType
  size: number
}

export const fileUrl = (id: string) => `/api/files/${id}`

/**
 * Files people add, such as images pasted on a whiteboard. They are kept beside the event
 * log, not in it, and are never dropped: a rebuild of the projections keeps them, and old
 * versions still point at them. The id is random so its address can be loaded without the
 * token (an `<img>` cannot send one) yet cannot be guessed.
 */
export function setupFiles(db: Database): void {
  db.run(`CREATE TABLE IF NOT EXISTS files (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    mime TEXT NOT NULL,
    size INTEGER NOT NULL,
    sha256 TEXT NOT NULL,
    data BLOB NOT NULL,
    created_at INTEGER NOT NULL
  )`)
  db.run('CREATE INDEX IF NOT EXISTS files_hash ON files (project_id, sha256)')
}

export const isImageType = (mime: string): mime is ImageType =>
  (IMAGE_TYPES as readonly string[]).includes(mime)

/** UTF-8 text without NUL bytes, whatever the browser called it (it may say nothing). */
export function isText(data: Uint8Array): boolean {
  if (data.includes(0)) return false
  try {
    new TextDecoder('utf-8', { fatal: true }).decode(data)
    return true
  } catch {
    return false
  }
}

/**
 * Keep a file; the same bytes in the same project are kept once. Images are kept as the
 * type they say; with `text`, anything else that is UTF-8 text is kept as plain text.
 */
export function saveFile(
  db: Database,
  projectId: string,
  input: { mime: string; data: Uint8Array },
  now: number,
  options: { text?: boolean } = {},
): StoredFile {
  const given = input.mime.split(';')[0]!.trim().toLowerCase()
  let mime: FileType
  if (isImageType(given)) mime = given
  else if (options.text && !given.startsWith('image/') && isText(input.data)) mime = 'text/plain'
  else {
    throw new DomainError(options.text ? 'error.attachmentType' : 'error.fileType', {
      type: given || '?',
    })
  }
  const max = mime === 'text/plain' ? MAX_TEXT_FILE_BYTES : MAX_FILE_BYTES
  if (input.data.byteLength > max) {
    throw new DomainError('error.fileTooLarge', { max: max / 1024 / 1024 })
  }
  const sha256 = createHash('sha256').update(input.data).digest('hex')
  const existing = db
    .query<{ id: string }, [string, string, string]>(
      'SELECT id FROM files WHERE project_id = ? AND sha256 = ? AND mime = ?',
    )
    .get(projectId, sha256, mime)
  const size = input.data.byteLength
  if (existing) return { id: existing.id, url: fileUrl(existing.id), mime, size }
  const id = `fil_${randomBytes(18).toString('base64url')}`
  db.run(
    'INSERT INTO files (id, project_id, mime, size, sha256, data, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)',
    [id, projectId, mime, size, sha256, input.data, now],
  )
  return { id, url: fileUrl(id), mime, size }
}

export function readFile(db: Database, id: string): { mime: FileType; data: Uint8Array } | null {
  const row = db
    .query<{ mime: FileType; data: Uint8Array }, [string]>(
      'SELECT mime, data FROM files WHERE id = ?',
    )
    .get(id)
  return row ?? null
}

/** A file of this project, without its bytes; `null` when the project has no such file. */
export function fileInfo(
  db: Database,
  projectId: string,
  id: string,
): { mime: FileType; size: number } | null {
  return (
    db
      .query<{ mime: FileType; size: number }, [string, string]>(
        'SELECT mime, size FROM files WHERE id = ? AND project_id = ?',
      )
      .get(id, projectId) ?? null
  )
}

const DATA_URL = /^data:([\w/+.-]+);base64,(.*)$/s

/**
 * A whiteboard with every image kept inline (a data URL, as older pages and agents wrote
 * them) moved into a file and pointed at by its address. Anything else is left as it is;
 * content that is not a whiteboard is returned unchanged for the content check to refuse.
 */
export function storeInlineImages(
  content: string,
  save: (input: { mime: string; data: Uint8Array }) => StoredFile,
): string {
  if (!content.includes('"data:')) return content
  let board: { nodes?: unknown }
  try {
    board = JSON.parse(content) as { nodes?: unknown }
  } catch {
    return content
  }
  if (!Array.isArray(board.nodes)) return content
  let changed = false
  for (const node of board.nodes as { type?: unknown; data?: { src?: unknown } }[]) {
    const src = node?.type === 'image' ? node.data?.src : undefined
    if (typeof src !== 'string') continue
    const match = DATA_URL.exec(src)
    if (!match) continue
    const file = save({ mime: match[1]!, data: Buffer.from(match[2]!, 'base64') })
    node.data!.src = file.url
    changed = true
  }
  return changed ? JSON.stringify(board) : content
}
