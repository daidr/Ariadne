import { join } from 'node:path'

/** The built pages, by their path in the address (`/index.html`, `/assets/…`). */
export interface WebAssets {
  /** The file at `path`, or null when there is none. */
  get(path: string): Promise<Blob | null>
}

/** The pages built into a directory on disk (`packages/web/dist`). */
export function directoryAssets(dir: string): WebAssets {
  return {
    async get(path) {
      if (path.includes('..')) return null
      const file = Bun.file(join(dir, path))
      return (await file.exists()) ? file : null
    },
  }
}

/**
 * The pages carried inside the program: each path maps to where the program keeps that
 * file. The type follows the path asked for, not the name the program gave the file.
 */
export function embeddedAssets(files: Readonly<Record<string, string>>): WebAssets {
  return {
    async get(path) {
      const at = Object.hasOwn(files, path) ? files[path] : undefined
      return at ? Bun.file(at, { type: Bun.file(path).type }) : null
    },
  }
}

let embedded: WebAssets | null = null

/** The release program hands over its pages here before the server starts. */
export function embedWeb(assets: WebAssets) {
  embedded = assets
}

/** The pages the program carries, when it is the release program. */
export function embeddedWeb(): WebAssets | null {
  return embedded
}
