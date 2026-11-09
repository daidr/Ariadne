import type { Ariadne } from '@ariadne/core'
import { diskUsage } from '@ariadne/plugin-local'
import { existsSync, mkdirSync, readdirSync, rmSync, statSync } from 'node:fs'
import { basename, join } from 'node:path'
import { BadRequest } from './http'

export interface BackupInfo {
  name: string
  size: number
  createdAt: number
  /** Made by the daily backup, which keeps the newest few; the user's own stay until removed. */
  auto: boolean
}

/** Space Ariadne's data directory takes, by part, in bytes; `null` where it is not known. */
export interface DataUsage {
  home: string
  /** The event log and everything kept with it (files, artifacts), with its journal. */
  database: number
  /** The plugins' data: threads' worktrees and clones of hosted repositories. */
  plugins: number | null
  backups: BackupInfo[]
  backupsDir: string
}

const BACKUP_NAME = /^ariadne-\d{8}-\d{6}(-\d+)?(-auto)?\.db$/
const AUTO = '-auto.db'
/** How many of the daily backups are kept. */
export const AUTO_BACKUPS_KEPT = 7

const sameDay = (a: number, b: number) => new Date(a).toDateString() === new Date(b).toDateString()

const sizeOf = (path: string) => (existsSync(path) ? statSync(path).size : 0)

/** The database and the backups next to it, kept under `<home>/backups`. */
export class DataStore {
  constructor(
    private readonly app: Ariadne,
    private readonly home: string,
    private readonly databasePath: string,
  ) {}

  private get backupsDir() {
    return join(this.home, 'backups')
  }

  backups(): BackupInfo[] {
    if (!existsSync(this.backupsDir)) return []
    return readdirSync(this.backupsDir)
      .filter((name) => BACKUP_NAME.test(name))
      .map((name) => {
        const info = statSync(join(this.backupsDir, name))
        return { name, size: info.size, createdAt: info.mtimeMs, auto: name.endsWith(AUTO) }
      })
      .toSorted((a, b) => b.createdAt - a.createdAt)
  }

  async usage(): Promise<DataUsage> {
    const db = this.databasePath
    const plugins = join(this.home, 'plugins')
    return {
      home: this.home,
      database: sizeOf(db) + sizeOf(`${db}-wal`) + sizeOf(`${db}-shm`),
      plugins: existsSync(plugins) ? await diskUsage(plugins) : 0,
      backups: this.backups(),
      backupsDir: this.backupsDir,
    }
  }

  /** A copy of the database as of now, named by the time. */
  backup(options: { auto?: boolean; now?: number } = {}): BackupInfo {
    mkdirSync(this.backupsDir, { recursive: true })
    const tail = options.auto ? AUTO : '.db'
    const stamp = new Date(options.now ?? Date.now())
      .toISOString()
      .replace(/[-:]/g, '')
      .replace('T', '-')
      .replace(/\..*/, '')
    let name = `ariadne-${stamp}${tail}`
    for (let n = 2; existsSync(join(this.backupsDir, name)); n++) {
      name = `ariadne-${stamp}-${n}${tail}`
    }
    this.app.backupTo(join(this.backupsDir, name))
    return this.backups().find((backup) => backup.name === name)!
  }

  /**
   * The daily backup: one a day, on a day something was written since the last, and only
   * the newest `AUTO_BACKUPS_KEPT` of them are kept. Gives the backup made, if one was.
   */
  dailyBackup(now = Date.now()): BackupInfo | null {
    const daily = this.backups().filter((backup) => backup.auto)
    const last = daily[0]
    if (last && (sameDay(last.createdAt, now) || this.writtenAt() <= last.createdAt)) return null
    const made = this.backup({ auto: true, now })
    for (const old of this.backups()
      .filter((backup) => backup.auto)
      .slice(AUTO_BACKUPS_KEPT)) {
      rmSync(join(this.backupsDir, old.name))
    }
    return made
  }

  /** When the database was last written to. */
  private writtenAt(): number {
    const db = this.databasePath
    return Math.max(
      ...[db, `${db}-wal`].map((path) => (existsSync(path) ? statSync(path).mtimeMs : 0)),
    )
  }

  removeBackup(name: string): void {
    if (!BACKUP_NAME.test(name) || basename(name) !== name) throw new BadRequest('name')
    const path = join(this.backupsDir, name)
    if (!existsSync(path)) throw new BadRequest('name')
    rmSync(path)
  }
}

/** Makes the daily backup while the server runs: at the start, then looked at every hour. */
export class DailyBackup {
  private timer: ReturnType<typeof setInterval> | null = null

  constructor(
    private readonly data: DataStore,
    private readonly onError: (error: unknown) => void,
  ) {}

  start(): void {
    this.timer ??= setInterval(() => this.run(), 60 * 60 * 1000)
    this.run()
  }

  stop(): void {
    if (this.timer) clearInterval(this.timer)
    this.timer = null
  }

  private run() {
    try {
      this.data.dailyBackup()
    } catch (error) {
      this.onError(error)
    }
  }
}
