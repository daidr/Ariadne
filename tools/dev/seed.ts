import { mkdir, writeFile } from 'node:fs/promises'
import { join } from 'node:path'
import { MOCK_MODELS } from './mock-api'

const git = async (cwd: string, ...args: string[]) => {
  const child = Bun.spawn(['git', '-c', 'commit.gpgsign=false', '-c', 'core.hooksPath=', ...args], {
    cwd,
    windowsHide: true,
    stdout: 'pipe',
    stderr: 'pipe',
    env: {
      ...process.env,
      GIT_CONFIG_GLOBAL: process.platform === 'win32' ? 'NUL' : '/dev/null',
      GIT_CONFIG_NOSYSTEM: '1',
    },
  })
  const [stdout, stderr, code] = await Promise.all([
    new Response(child.stdout).text(),
    new Response(child.stderr).text(),
    child.exited,
  ])
  if (code) throw new Error(`Sandbox git ${args[0]}: ${stderr}`)
  return stdout.trim()
}

/** Only writes under the new sandbox home; no user repository or settings are used. */
export async function seedSandbox(home: string, baseUrl: string) {
  await writeFile(
    join(home, 'config.json'),
    JSON.stringify(
      {
        locale: 'zh-CN',
        search: {
          provider: 'deepseek',
          services: {
            tavily: { baseUrl, apiKey: 'dev-only' },
            deepseek: { baseUrl, apiKey: 'dev-only' },
          },
        },
        models: {
          providers: [
            {
              id: 'dev',
              kind: 'openai-compatible',
              name: '开发模拟 API',
              settings: { baseUrl, apiKey: 'dev-only' },
            },
          ],
          models: MOCK_MODELS.map((model) => ({
            id: model,
            provider: 'dev',
            model,
            contextTokens: 128_000,
          })),
          roles: { thread: { model: 'mock', effort: null, fallback: null } },
        },
      },
      null,
      2,
    ),
    { mode: 0o600 },
  )
  const repo = join(home, 'example-repo')
  await mkdir(repo)
  await git(repo, 'init', '-q', '-b', 'main')
  await git(repo, 'config', 'user.name', 'Ariadne Dev')
  await git(repo, 'config', 'user.email', 'dev@example.invalid')
  await writeFile(join(repo, 'README.md'), '# 开发沙箱\n\n这里是一个独立的示例仓库。\n')
  await git(repo, 'add', '.')
  await git(repo, 'commit', '-qm', 'Initial sandbox')

  const result = Bun.spawn(['go', 'run', './tools/sandboxseed', home, repo], {
    cwd: join(import.meta.dir, '../..'),
    stdin: 'ignore',
    stdout: 'inherit',
    stderr: 'inherit',
    windowsHide: true,
  })
  if ((await result.exited) !== 0) throw new Error('Could not seed the Go development sandbox')
}
