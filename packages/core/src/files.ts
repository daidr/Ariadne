import type { Database } from 'bun:sqlite'
import { createHash, randomBytes } from 'node:crypto'
import { DomainError } from './errors'

/** The images a whiteboard can hold. SVG is left out: it can carry script. */
export const FILE_TYPES = [
  'image/png',
  'image/jpeg',
  'image/gif',
  'image/webp',
  'image/avif',
] as const
export type FileType = (typeof FILE_TYPES)[number]

export const MAX_FILE_BYTES = 10 * 1024 * 1024

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

const isFileType = (mime: string): mime is FileType =>
  (FILE_TYPES as readonly string[]).includes(mime)

/** Keep a file; the same bytes in the same project are kept once. */
export function saveFile(
  db: Database,
  projectId: string,
  input: { mime: string; data: Uint8Array },
  now: number,
): StoredFile {
  const mime = input.mime.split(';')[0]!.trim().toLowerCase()
  if (!isFileType(mime)) throw new DomainError('error.fileType', { type: mime || '?' })
  if (input.data.byteLength > MAX_FILE_BYTES) {
    throw new DomainError('error.fileTooLarge', { max: MAX_FILE_BYTES / 1024 / 1024 })
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
