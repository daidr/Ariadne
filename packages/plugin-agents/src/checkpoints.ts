import { createHash, randomUUID } from 'node:crypto'
import {
  mkdirSync,
  readFileSync,
  readdirSync,
  renameSync,
  rmSync,
  statSync,
  writeFileSync,
} from 'node:fs'
import { join } from 'node:path'

export interface Checkpoint {
  config: string
  system: string
  fps: string[]
  driver: unknown
}

/** Completed sessions only. Active turns are never recovered from an older checkpoint. */
export class Checkpoints {
  private readonly dir: string
  private readonly files = new Map<string, number>()
  constructor(
    dataDir: string,
    kind: string,
    private readonly limit = 256,
  ) {
    this.dir = join(dataDir, 'checkpoints', kind)
    mkdirSync(this.dir, { recursive: true, mode: 0o700 })
    for (const name of readdirSync(this.dir)) {
      if (!name.endsWith('.json')) continue
      const path = join(this.dir, name)
      this.files.set(path, statSync(path).mtimeMs)
    }
    this.prune()
  }
  private path(key: string) {
    return join(this.dir, `${createHash('sha256').update(key).digest('hex')}.json`)
  }
  read(key: string): Checkpoint | null {
    try {
      const path = this.path(key)
      const info = statSync(path)
      if (info.size > 4 * 1024 * 1024 || Date.now() - info.mtimeMs > 30 * 86400_000) {
        this.remove(key)
        return null
      }
      const value = JSON.parse(readFileSync(path, 'utf8'))
      return typeof value.config === 'string' &&
        typeof value.system === 'string' &&
        Array.isArray(value.fps) &&
        value.fps.every((fp: unknown) => typeof fp === 'string') &&
        value.driver
        ? value
        : null
    } catch {
      return null
    }
  }
  write(key: string, value: Checkpoint) {
    const text = JSON.stringify(value)
    if (Buffer.byteLength(text) > 4 * 1024 * 1024) return this.remove(key)
    const path = this.path(key)
    const temp = `${path}.${randomUUID()}.tmp`
    try {
      writeFileSync(temp, text, { mode: 0o600 })
      renameSync(temp, path)
      this.files.delete(path)
      this.files.set(path, Date.now())
      this.prune()
    } finally {
      rmSync(temp, { force: true })
    }
  }
  remove(key: string) {
    const path = this.path(key)
    this.files.delete(path)
    rmSync(path, { force: true })
  }
  private prune() {
    const files = [...this.files].toReversed().toSorted((a, b) => b[1] - a[1])
    files.forEach(([path, at], index) => {
      if (index >= this.limit || Date.now() - at > 30 * 86400_000) {
        this.files.delete(path)
        rmSync(path, { force: true })
      }
    })
  }
}
