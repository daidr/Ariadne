import type { Ariadne } from '@ariadne/core'
import { diskUsage } from '@ariadne/plugin-local'
import { existsSync, mkdirSync, readdirSync, rmSync, statSync } from 'node:fs'
import { basename, join } from 'node:path'
import { BadRequest } from './http'

export interface BackupInfo {
  name: string
  size: number
  createdAt: number
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

const BACKUP_NAME = /^ariadne-\d{8}-\d{6}(-\d+)?\.db$/

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
        return { name, size: info.size, createdAt: info.mtimeMs }
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
  backup(): BackupInfo {
    mkdirSync(this.backupsDir, { recursive: true })
    const stamp = new Date()
      .toISOString()
      .replace(/[-:]/g, '')
      .replace('T', '-')
      .replace(/\..*/, '')
    let name = `ariadne-${stamp}.db`
    for (let n = 2; existsSync(join(this.backupsDir, name)); n++) name = `ariadne-${stamp}-${n}.db`
    this.app.backupTo(join(this.backupsDir, name))
    return this.backups().find((backup) => backup.name === name)!
  }

  removeBackup(name: string): void {
    if (!BACKUP_NAME.test(name) || basename(name) !== name) throw new BadRequest('name')
    const path = join(this.backupsDir, name)
    if (!existsSync(path)) throw new BadRequest('name')
    rmSync(path)
  }
}
