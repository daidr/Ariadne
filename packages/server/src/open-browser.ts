/** The command that opens `url` in the user's browser on this platform, or `null`. */
export function openCommand(
  url: string,
  platform: NodeJS.Platform = process.platform,
  env: Record<string, string | undefined> = process.env,
): string[] | null {
  if (platform === 'darwin') return ['open', url]
  // Not through a shell: the address would have to be quoted for it.
  if (platform === 'win32') return ['rundll32', 'url.dll,FileProtocolHandler', url]
  // Without a display there is no browser to open: a server reached over SSH, a container.
  if (!env.DISPLAY && !env.WAYLAND_DISPLAY) return null
  return ['xdg-open', url]
}

/** Open `url` in the user's browser; whether a command for it was started. */
export function openBrowser(url: string): boolean {
  const command = openCommand(url)
  if (!command || !Bun.which(command[0]!)) return false
  try {
    Bun.spawn(command, {
      stdin: 'ignore',
      stdout: 'ignore',
      stderr: 'ignore',
      windowsHide: true,
    }).unref()
    return true
  } catch {
    return false
  }
}
