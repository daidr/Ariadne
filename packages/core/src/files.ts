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
