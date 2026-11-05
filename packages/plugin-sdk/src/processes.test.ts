import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { elapsedSeconds, ProcessLedger, startTimes } from './processes'

const posix = process.platform !== 'win32'

const alive = (pid: number) => {
  try {
    process.kill(pid, 0)
    return true
  } catch {
    return false
  }
}

/** A process with a child of its own in a group of its own, as a thread's command runs. */
function spawnGroup() {
  return Bun.spawn(['sh', '-c', 'sleep 60 & sleep 60'], {
    stdout: 'ignore',
    stderr: 'ignore',
    stdin: 'ignore',
    detached: true,
    windowsHide: true,
  })
}

/** A pid no process has now: one that just exited. */
async function deadPid() {
  const proc = Bun.spawn(['true'], { stdout: 'ignore', windowsHide: true })
  await proc.exited
  return proc.pid
}

test('reads every form of ps etime', () => {
  expect(elapsedSeconds('00:05')).toBe(5)
  expect(elapsedSeconds('12:05')).toBe(725)
  expect(elapsedSeconds('01:00:00')).toBe(3600)
  expect(elapsedSeconds('2-01:00:01')).toBe(2 * 86400 + 3601)
  expect(elapsedSeconds('soon')).toBeNull()
})

describe.runIf(posix)('processes left by a server that died', () => {
  let dir: string
  let path: string
  const started: ReturnType<typeof spawnGroup>[] = []

  beforeEach(() => {
    dir = mkdtempSync(join(tmpdir(), 'ariadne-processes-'))
    path = join(dir, 'processes.json')
  })
  afterEach(() => {
    for (const proc of started) {
      try {
        process.kill(-proc.pid, 'SIGKILL')
      } catch {
        // Gone.
      }
    }
    started.length = 0
    rmSync(dir, { recursive: true, force: true })
  })

  test('are killed with what they started on the next start', async () => {
    const proc = spawnGroup()
    started.push(proc)
    await Bun.sleep(100)
    writeFileSync(
      path,
      JSON.stringify({
        owner: { pid: await deadPid(), startedAt: Date.now() - 60_000 },
        processes: [{ pid: proc.pid, group: true, startedAt: Date.now(), label: 'command' }],
      }),
    )
    const ledger = new ProcessLedger()
    const leftovers = await ledger.open(path)
    expect(leftovers.killed.map((entry) => entry.pid)).toEqual([proc.pid])
    await proc.exited
    // The child it started is gone too, once the system has reaped it.
    const groupGone = async () => {
      for (let i = 0; i < 50; i++) {
        try {
          process.kill(-proc.pid, 0)
        } catch {
          return true
        }
        await Bun.sleep(50)
      }
      return false
    }
    expect(await groupGone()).toBe(true)
    // The file is this server's now.
    expect(JSON.parse(readFileSync(path, 'utf8'))).toMatchObject({
      owner: { pid: process.pid },
      processes: [],
    })
  })

  test('leave alone a process that got the pid later, and its group', async () => {
    const proc = spawnGroup()
    started.push(proc)
    writeFileSync(
      path,
      JSON.stringify({
        owner: { pid: await deadPid(), startedAt: 0 },
        processes: [{ pid: proc.pid, group: true, startedAt: Date.now() - 86_400_000, label: 'x' }],
      }),
    )
    const leftovers = await new ProcessLedger().open(path)
    expect(leftovers.killed).toEqual([])
    expect(alive(proc.pid)).toBe(true)
  })

  test('leave alone what a server still running keeps', async () => {
    const proc = spawnGroup()
    started.push(proc)
    const owner = spawnGroup()
    started.push(owner)
    await Bun.sleep(100)
    const ownerStart = (await startTimes([owner.pid])).get(owner.pid)!
    const file = JSON.stringify({
      owner: { pid: owner.pid, startedAt: ownerStart },
      processes: [{ pid: proc.pid, group: true, startedAt: Date.now(), label: 'command' }],
    })
    writeFileSync(path, file)
    const ledger = new ProcessLedger()
    expect(await ledger.open(path)).toEqual({ killed: [], ownerAlive: true })
    expect(alive(proc.pid)).toBe(true)
    ledger.track(12345, { group: false, label: 'x' })
    expect(readFileSync(path, 'utf8')).toBe(file)
  })

  test('are written as they start and forgotten as they end', async () => {
    const ledger = new ProcessLedger()
    await ledger.open(path)
    const release = ledger.track(4242, { group: true, label: 'mcp' })
    expect(JSON.parse(readFileSync(path, 'utf8')).processes).toMatchObject([
      { pid: 4242, group: true, label: 'mcp' },
    ])
    release()
    expect(JSON.parse(readFileSync(path, 'utf8')).processes).toEqual([])
  })
})
