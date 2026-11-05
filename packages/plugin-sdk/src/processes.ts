import { existsSync, readFileSync, renameSync, writeFileSync } from 'node:fs'

const windows = process.platform === 'win32'

/**
 * How far apart the start time the system reports and the one recorded may be for a
 * process to count as the same one, and not a later one that got its pid.
 */
const SAME_START_MS = 3000

/** A process the server started that should not outlive it. */
export interface TrackedProcess {
  pid: number
  /** It leads a process group of its own: what it started goes with it. */
  group: boolean
  /** When it was started, by the server's clock. */
  startedAt: number
  /** What it is, for the log: e.g. `command` or `mcp`. */
  label: string
}

interface LedgerFile {
  owner: { pid: number; startedAt: number }
  processes: TrackedProcess[]
}

/** What `open` found the last server had left running, and what it did about it. */
export interface Leftovers {
  /** Killed: still running, and still the process that was started. */
  killed: TrackedProcess[]
  /** Another server still runs with this ledger, so nothing of it was touched. */
  ownerAlive: boolean
}

/**
 * The processes the server has running that would be left behind if it died: background
 * commands, long builds, tool servers. They are written to a file as they start and end,
 * and the next start kills what the last one left, so a crash does not leave a dev server
 * or a tool server running with nobody to stop it. Until `open` is called (in tests, for
 * one) it only keeps them in memory.
 */
export class ProcessLedger {
  private path: string | null = null
  private readonly running = new Map<number, TrackedProcess>()
  private readonly startedAt = Date.now()

  /**
   * Keep the ledger at `path` from now on. What a server that is no longer running left
   * there is killed first.
   */
  async open(path: string): Promise<Leftovers> {
    const leftovers = await reap(path)
    // That server keeps its ledger; this one's processes are only known in memory.
    if (leftovers.ownerAlive) return leftovers
    this.path = path
    this.write()
    return leftovers
  }

  /** Record a process that was just started; the returned function forgets it again. */
  track(pid: number | undefined, options: { group: boolean; label: string }): () => void {
    if (!pid) return () => {}
    const entry = { pid, startedAt: Date.now(), ...options }
    this.running.set(pid, entry)
    this.write()
    return () => {
      // A later process may have got the pid by now; that one stays.
      if (this.running.get(pid) !== entry) return
      this.running.delete(pid)
      this.write()
    }
  }

  list(): TrackedProcess[] {
    return [...this.running.values()]
  }

  private write() {
    if (!this.path) return
    const file: LedgerFile = {
      owner: { pid: process.pid, startedAt: this.startedAt },
      processes: this.list(),
    }
    // A crash halfway through a write leaves the old file whole, not half a new one.
    const temporary = `${this.path}.${process.pid}.tmp`
    try {
      writeFileSync(temporary, JSON.stringify(file))
      renameSync(temporary, this.path)
    } catch (error) {
      console.error('could not write the process ledger', error)
    }
  }
}

/** The server's one ledger; plugins that start long-lived processes record them here. */
export const processes = new ProcessLedger()

async function reap(path: string): Promise<Leftovers> {
  const none: Leftovers = { killed: [], ownerAlive: false }
  if (!existsSync(path)) return none
  let file: LedgerFile
  try {
    file = JSON.parse(readFileSync(path, 'utf8')) as LedgerFile
  } catch {
    return none
  }
  if (!file?.owner || !Array.isArray(file.processes)) return none
  const pids = [file.owner.pid, ...file.processes.map((entry) => entry.pid)]
  const started = await startTimes(pids)
  const same = (pid: number, at: number) => {
    const actual = started.get(pid)
    return actual !== undefined && Math.abs(actual - at) <= SAME_START_MS
  }
  if (file.owner.pid !== process.pid && same(file.owner.pid, file.owner.startedAt)) {
    return { killed: [], ownerAlive: true }
  }
  const killed: TrackedProcess[] = []
  for (const entry of file.processes) {
    if (same(entry.pid, entry.startedAt)) {
      kill(entry)
      killed.push(entry)
    } else if (entry.group && !windows && groupExists(entry.pid)) {
      // The leader ended, what it started still runs. A group's id is not given to another
      // process while the group exists, so it is still that group.
      kill(entry)
      killed.push(entry)
    }
  }
  return { killed, ownerAlive: false }
}

function kill(entry: TrackedProcess) {
  if (windows) {
    Bun.spawnSync(['taskkill', '/T', '/F', '/PID', String(entry.pid)], {
      stdout: 'ignore',
      stderr: 'ignore',
      stdin: 'ignore',
      windowsHide: true,
    })
    return
  }
  try {
    process.kill(entry.group ? -entry.pid : entry.pid, 'SIGKILL')
  } catch {
    // Gone in the meantime.
  }
}

function groupExists(pgid: number): boolean {
  try {
    process.kill(-pgid, 0)
    return true
  } catch {
    // No such group, or (EPERM) one that is not ours, so not the group that was started.
    return false
  }
}

/** When each of `pids` that still runs was started, in ms since the epoch. */
export async function startTimes(pids: number[]): Promise<Map<number, number>> {
  const result = new Map<number, number>()
  const wanted = [...new Set(pids.filter((pid) => Number.isInteger(pid) && pid > 0))]
  if (!wanted.length) return result
  const now = Date.now()
  if (windows) {
    const script = `Get-Process -Id ${wanted.join(',')} -ErrorAction SilentlyContinue | ForEach-Object { "$($_.Id) $([DateTimeOffset]::new($_.StartTime.ToUniversalTime()).ToUnixTimeMilliseconds())" }`
    const out = await output(['powershell', '-NoProfile', '-NonInteractive', '-Command', script])
    for (const line of out.split(/\r?\n/)) {
      const [pid, at] = line.trim().split(/\s+/).map(Number)
      if (pid && at) result.set(pid, at)
    }
    return result
  }
  // `etime` is in every ps, as [[dd-]hh:]mm:ss since the process started.
  const out = await output(['ps', '-o', 'pid=,etime=', '-p', wanted.join(',')])
  for (const line of out.split('\n')) {
    const [pid, etime] = line.trim().split(/\s+/)
    const seconds = etime ? elapsedSeconds(etime) : null
    if (pid && seconds !== null) result.set(Number(pid), now - seconds * 1000)
  }
  return result
}

/** Seconds of a ps `etime`, `[[dd-]hh:]mm:ss`. */
export function elapsedSeconds(etime: string): number | null {
  const match = /^(?:(?:(\d+)-)?(\d+):)?(\d+):(\d+)$/.exec(etime)
  if (!match) return null
  const [, days, hours, minutes, seconds] = match
  return (
    ((Number(days ?? 0) * 24 + Number(hours ?? 0)) * 60 + Number(minutes)) * 60 + Number(seconds)
  )
}

async function output(argv: string[]): Promise<string> {
  try {
    const proc = Bun.spawn(argv, {
      stdout: 'pipe',
      stderr: 'ignore',
      stdin: 'ignore',
      windowsHide: true,
      env: { ...process.env, LC_ALL: 'C' },
    })
    const text = await new Response(proc.stdout).text()
    await proc.exited
    return text
  } catch {
    return ''
  }
}
