import { McpClient } from '../client'
import { StdioTransport } from '../stdio'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const { argv } = (await Bun.stdin.json()) as { argv: string[] }
const directory = mkdtempSync(join(tmpdir(), 'ariadne-go-mcp-oracle-'))
const client = new McpClient(
  new StdioTransport({ argv, cwd: directory, env: process.env as Record<string, string> }),
)
try {
  const output: Record<string, unknown> = {
    info: await client.connect(),
    tools: await client.listTools(),
  }
  for (const name of ['echo', 'picture', 'fail'])
    output[name] = await client.callTool(name, { text: '中文' })
  console.info(JSON.stringify(output))
} finally {
  await client.close()
  rmSync(directory, { recursive: true, force: true, maxRetries: 10, retryDelay: 50 })
}
