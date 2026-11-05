/** PowerShell, by its program name: 7 (`pwsh`) or the 5.1 that comes with Windows. */
const POWERSHELLS = new Set(['pwsh', 'powershell', 'pwsh.exe', 'powershell.exe'])

/**
 * The shell commands run in when the project did not pick one: bash on Linux and macOS;
 * on Windows PowerShell 7 when it is installed, else the PowerShell 5.1 Windows has.
 */
export function defaultShell(
  platform: NodeJS.Platform = process.platform,
  which: (program: string) => string | null = Bun.which,
): { name: 'bash' | 'pwsh' | 'powershell'; argv: string[] } {
  if (platform !== 'win32') return { name: 'bash', argv: ['bash', '-c'] }
  const name = which('pwsh') ? 'pwsh' : 'powershell'
  return { name, argv: [name, '-NoProfile', '-NonInteractive', '-Command'] }
}

/**
 * The program and arguments that run `command` in `shell`. PowerShell gets the command
 * encoded (`-EncodedCommand`), so no quote in it is taken apart on the way to its parser,
 * and prints UTF-8 whatever the console's code page.
 */
export function shellArgv(shell: readonly string[], command: string): string[] {
  const [program = '', ...flags] = shell
  const name = program.split(/[\\/]/).pop()!.toLowerCase()
  if (!POWERSHELLS.has(name) || flags.at(-1)?.toLowerCase() !== '-command') {
    return [...shell, command]
  }
  const utf8 = '[Console]::OutputEncoding = $OutputEncoding = [Text.UTF8Encoding]::new($false)\n'
  const encoded = Buffer.from(utf8 + command, 'utf16le').toString('base64')
  return [program, ...flags.slice(0, -1), '-EncodedCommand', encoded]
}
