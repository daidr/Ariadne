/** The images a whiteboard or a message can hold. SVG is left out: it can carry script. */
export const IMAGE_TYPES = [
  'image/png',
  'image/jpeg',
  'image/gif',
  'image/webp',
  'image/avif',
] as const

export type ImageType = (typeof IMAGE_TYPES)[number]

/** A stored attachment's MIME type (images, text, archives, documents, media, or binary). */
export type FileType = string

export const MAX_FILE_BYTES = 50 * 1024 * 1024

export const MAX_IMAGE_BYTES = 10 * 1024 * 1024

/** Inline text preview budget for attachments. */
export const MAX_TEXT_FILE_BYTES = 1024 * 1024

export interface StoredFile {
  id: string
  /** Where the page loads it from. */
  url: string
  mime: FileType
  size: number
}

export const fileUrl = (id: string) => `/api/files/${id}`
