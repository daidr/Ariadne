import { MAX_FILE_BYTES } from '@ariadne/core'
import type { Workspace } from '@ariadne/plugin-sdk'
import { splitCode } from './markdown-code'

/** Image files by extension, checked against their first bytes before anyone sees them. */
export const IMAGE_EXTENSIONS: Record<
  string,
  { mime: string; magic: (bytes: Uint8Array) => boolean }
> = {
  png: { mime: 'image/png', magic: (b) => b[0] === 0x89 && b[1] === 0x50 && b[2] === 0x4e },
  jpg: { mime: 'image/jpeg', magic: (b) => b[0] === 0xff && b[1] === 0xd8 },
  jpeg: { mime: 'image/jpeg', magic: (b) => b[0] === 0xff && b[1] === 0xd8 },
  gif: { mime: 'image/gif', magic: (b) => b[0] === 0x47 && b[1] === 0x49 && b[2] === 0x46 },
  webp: {
    mime: 'image/webp',
    magic: (b) => b[0] === 0x52 && b[1] === 0x49 && b[8] === 0x57 && b[9] === 0x45,
  },
}

export function imageType(path: string) {
  return IMAGE_EXTENSIONS[path.split('.').pop()?.toLowerCase() ?? ''] ?? null
}

/** Images a message may carry; more stay as their text. */
const MAX_IMAGES = 20

/** `[text](address)` or `![alt](address)`, the address optionally in `<…>`, maybe a title. */
const LINK = /(!?\[[^\]\n]*\]\(\s*)(<[^>\n]+>|[^\s()<>]+)((?:\s+"[^"\n]*")?\s*\))/g

/** An address naming a workspace file: no scheme, no `//host`, no anchor. */
function workspacePath(address: string): string | null {
  const path = address.startsWith('<') ? address.slice(1, -1).trim() : address
  if (!path || /^[a-z][\w+.-]*:/i.test(path) || path.startsWith('//') || path.startsWith('#')) {
    return null
  }
  if (!imageType(path)) return null
  try {
    return decodeURI(path)
  } catch {
    return path
  }
}

/**
 * A message with its workspace images kept: `![alt](shots/a.png)` and `[text](shots/a.png)`
 * become `file:fil_…` links to a copy in the project's files. The copy is what the user
 * sees, then and later, whatever happens to the workspace. Paths are read through the
 * workspace, so nothing outside it can be shown; a file that is missing, too large or not
 * the image its name says is left as it was, and shows as its text.
 */
export async function keepMessageImages(
  content: string,
  workspace: () => Promise<Workspace>,
  save: (file: { mime: string; data: Uint8Array }) => { id: string },
): Promise<string> {
  // Code, where a link is only text, stays as it is.
  const parts = splitCode(content)
  const paths = new Set<string>()
  for (const [index, part] of parts.entries()) {
    if (index % 2) continue
    for (const match of part.matchAll(LINK)) {
      const path = workspacePath(match[2]!)
      if (path && paths.size < MAX_IMAGES) paths.add(path)
    }
  }
  if (!paths.size) return content
  let opened: Workspace
  try {
    opened = await workspace()
  } catch {
    return content
  }
  const kept = new Map<string, string>()
  for (const path of paths) {
    const type = imageType(path)!
    try {
      const data = await opened.readBytes(path, MAX_FILE_BYTES)
      if (type.magic(data)) kept.set(path, save({ mime: type.mime, data }).id)
    } catch {
      // Left as text.
    }
  }
  if (!kept.size) return content
  return parts
    .map((part, index) =>
      index % 2
        ? part
        : part.replace(LINK, (whole, open: string, address: string, close: string) => {
            const path = workspacePath(address)
            const id = path && kept.get(path)
            return id ? `${open}file:${id}${close}` : whole
          }),
    )
    .join('')
}
