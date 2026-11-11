import { STDIO_COMMAND, stdioBridge } from '@ariadne/plugin-agents/mcp-stdio'
import { embeddedAssets, embedWeb } from './web-assets'

/**
 * Where the release program starts (`bun run release` writes the entry that calls this):
 * the pages come from inside the program, and the page opens by itself like `bun run start`.
 *
 * - `ariadne mcp-stdio <address>`: the tool bridge for agents that only start local programs.
 * - `ariadne self-check`: says whether the pages are in the program, for the build to check
 *   the compressed program still runs.
 */
export async function runRelease(files: Readonly<Record<string, string>>) {
  const [command, ...args] = process.argv.slice(2)
  const assets = embeddedAssets(files)
  if (command === STDIO_COMMAND) {
    await stdioBridge(args[0])
    return
  }
  if (command === 'self-check') {
    const shell = await assets.get('/index.html')
    const ok = !!shell && (await shell.text()).includes('<script')
    console.info(ok ? `ok ${Object.keys(files).length}` : 'missing pages')
    process.exit(ok ? 0 : 1)
  }
  embedWeb(assets)
  process.env.ARIADNE_OPEN ??= '1'
  await import('./main')
}
