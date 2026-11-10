import { existsSync } from 'node:fs'
import { chmod, mkdir, mkdtemp, readdir, rename, rm } from 'node:fs/promises'
import { homedir } from 'node:os'
import { join } from 'node:path'

/** Where a headless browser comes from; swapped in tests. */
export interface BrowserLocator {
  /** A browser on the machine, or `null`. */
  installed(): string | null
  /** The one downloaded before, or downloaded now. */
  download(signal: AbortSignal): Promise<string>
}

/** Chrome, Edge or Chromium where installers put them, by platform. */
export function installedBrowser(
  platform: NodeJS.Platform = process.platform,
  env: Record<string, string | undefined> = process.env,
  exists: (path: string) => boolean = existsSync,
  which: (name: string) => string | null = (name) => Bun.which(name),
): string | null {
  const candidates: string[] = []
  if (platform === 'win32') {
    const roots = [env.PROGRAMFILES, env['PROGRAMFILES(X86)'], env.LOCALAPPDATA].filter(
      (root): root is string => !!root,
    )
    for (const root of roots) {
      candidates.push(
        join(root, 'Google', 'Chrome', 'Application', 'chrome.exe'),
        join(root, 'Microsoft', 'Edge', 'Application', 'msedge.exe'),
        join(root, 'Chromium', 'Application', 'chrome.exe'),
      )
    }
  } else if (platform === 'darwin') {
    for (const root of ['/Applications', join(homedir(), 'Applications')]) {
      candidates.push(
        join(root, 'Google Chrome.app', 'Contents', 'MacOS', 'Google Chrome'),
        join(root, 'Microsoft Edge.app', 'Contents', 'MacOS', 'Microsoft Edge'),
        join(root, 'Chromium.app', 'Contents', 'MacOS', 'Chromium'),
      )
    }
  } else {
    for (const name of [
      'google-chrome',
      'google-chrome-stable',
      'chromium',
      'chromium-browser',
      'microsoft-edge',
      'microsoft-edge-stable',
    ]) {
      const found = which(name)
      if (found) return found
    }
  }
  return candidates.find((path) => exists(path)) ?? null
}

/** Chrome for Testing's name for the platform, or `null` where it has no build. */
export function testingPlatform(
  platform: NodeJS.Platform = process.platform,
  arch: string = process.arch,
): string | null {
  if (platform === 'linux') return arch === 'x64' ? 'linux64' : null
  if (platform === 'darwin') return arch === 'arm64' ? 'mac-arm64' : 'mac-x64'
  if (platform === 'win32') return arch === 'ia32' ? 'win32' : 'win64'
  return null
}

const VERSIONS =
  'https://googlechromelabs.github.io/chrome-for-testing/last-known-good-versions-with-downloads.json'

/**
 * Finds the machine's own browser first; without one, downloads Chrome for Testing's
 * headless shell into `dir` the first time it is needed, once even when asked twice.
 */
export function browserLocator(dir: string, versions = VERSIONS): BrowserLocator {
  // One download for everyone who asks; it is called off only when all of them gave up.
  let downloading: { done: Promise<string>; stop: AbortController; waiting: number } | null = null
  const shellPath = (platform: string) =>
    join(
      dir,
      `chrome-headless-shell-${platform}`,
      process.platform === 'win32' ? 'chrome-headless-shell.exe' : 'chrome-headless-shell',
    )
  return {
    installed: () => installedBrowser(),
    download(signal) {
      const platform = testingPlatform()
      if (!platform) {
        return Promise.reject(new Error(`no headless browser download for ${process.platform}`))
      }
      const path = shellPath(platform)
      if (existsSync(path)) return Promise.resolve(path)
      if (signal.aborted) return Promise.reject(signal.reason)
      if (!downloading) {
        const stop = new AbortController()
        const entry = {
          stop,
          waiting: 0,
          done: fetchShell(dir, platform, versions, stop.signal)
            .then(() => {
              if (!existsSync(path)) throw new Error('the download did not hold the browser')
              return path
            })
            .finally(() => {
              if (downloading === entry) downloading = null
            }),
        }
        downloading = entry
      }
      const shared = downloading
      shared.waiting++
      const gaveUp = Promise.withResolvers<never>()
      const leave = () => {
        gaveUp.reject(signal.reason)
        if (--shared.waiting === 0) shared.stop.abort(signal.reason)
      }
      signal.addEventListener('abort', leave, { once: true })
      return Promise.race([shared.done, gaveUp.promise]).finally(() =>
        signal.removeEventListener('abort', leave),
      )
    },
  }
}

async function fetchShell(
  dir: string,
  platform: string,
  versions: string,
  signal: AbortSignal,
): Promise<void> {
  const listing = (await (await fetch(versions, { signal })).json()) as {
    channels?: Record<string, { downloads?: Record<string, { platform: string; url: string }[]> }>
  }
  const url = listing.channels?.Stable?.downloads?.['chrome-headless-shell']?.find(
    (entry) => entry.platform === platform,
  )?.url
  if (!url) throw new Error(`no headless browser listed for ${platform}`)
  const response = await fetch(url, { signal })
  if (!response.ok) throw new Error(`download failed: HTTP ${response.status}`)
  await mkdir(dir, { recursive: true })
  // Unpacked beside the final place, then moved in: a half-done download is never used.
  const work = await mkdtemp(join(dir, 'download-'))
  try {
    const zip = join(work, 'shell.zip')
    await Bun.write(zip, response)
    // Windows 10 and later ship bsdtar, which reads zip (named in full: a GNU tar from Git
    // may come first on PATH and cannot); elsewhere unzip is at hand.
    const argv =
      process.platform === 'win32'
        ? [
            join(process.env.SystemRoot ?? 'C:\\Windows', 'System32', 'tar.exe'),
            '-xf',
            zip,
            '-C',
            work,
          ]
        : ['unzip', '-q', '-o', zip, '-d', work]
    const unpack = Bun.spawn(argv, { stdout: 'ignore', stderr: 'pipe', windowsHide: true })
    if ((await unpack.exited) !== 0) {
      throw new Error(`could not unpack: ${(await new Response(unpack.stderr).text()).trim()}`)
    }
    const folder = `chrome-headless-shell-${platform}`
    if (!(await readdir(work)).includes(folder)) throw new Error('unexpected download layout')
    await rm(join(dir, folder), { recursive: true, force: true })
    await rename(join(work, folder), join(dir, folder))
    if (process.platform !== 'win32') {
      await chmod(join(dir, folder, 'chrome-headless-shell'), 0o755)
    }
  } finally {
    await rm(work, { recursive: true, force: true })
  }
}
