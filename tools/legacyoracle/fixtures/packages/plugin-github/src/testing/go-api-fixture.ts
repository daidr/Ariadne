import { GitHubApi, GitHubError } from '../api'
import { githubProvider, parsePullRequestUrl, parseRepository } from '../index'

interface Call {
  method: string
  args?: unknown[]
}
interface Scenario {
  calls: Call[]
  responses: Record<string, { body: unknown; status?: number }>
}
const input = (await Bun.stdin.json()) as {
  scenarios: Scenario[]
  repositories: string[]
  links: { url: string; web: string }[]
}
const scenarios = []
for (const scenario of input.scenarios) {
  const requests: { method: string; path: string; body: unknown; authorization: string | null }[] =
    []
  const api = new GitHubApi({
    base: 'http://fixture',
    owner: 'acme',
    name: 'app',
    token: async () => 'private-token',
    fetch: (async (address, options) => {
      const url = new URL(String(address))
      const method = options?.method ?? 'GET'
      requests.push({
        method,
        path: url.pathname + url.search,
        body: options?.body ? (JSON.parse(String(options.body)) as unknown) : null,
        authorization: new Headers(options?.headers).get('authorization'),
      })
      const response = scenario.responses[`${method} ${url.pathname}${url.search}`]
      if (!response) throw new Error(`unexpected request ${method} ${url.pathname}${url.search}`)
      return new Response(
        typeof response.body === 'string' ? response.body : JSON.stringify(response.body),
        { status: response.status ?? 200 },
      )
    }) as typeof fetch,
  })
  const results = []
  for (const call of scenario.calls) {
    try {
      const args = call.args ?? []
      let result: unknown
      switch (call.method) {
        case 'repository':
          result = await api.repository()
          break
        case 'pullRequestFor':
          result = await api.pullRequestFor(args[0] as string)
          break
        case 'pullRequest':
          result = await api.pullRequest(args[0] as number)
          break
        case 'pullRequestPreview':
          result = await api.pullRequestPreview(args[0] as number)
          break
        case 'openPullRequest':
          result = await api.openPullRequest(args[0] as Parameters<typeof api.openPullRequest>[0])
          break
        case 'mergePullRequest':
          result = await api.mergePullRequest(
            args[0] as number,
            args[1] as Parameters<typeof api.mergePullRequest>[1],
          )
          break
        case 'checks':
          result = await api.checks(args[0] as string)
          break
        case 'issues':
          result = await api.issues(args[0] as Parameters<typeof api.issues>[0])
          break
        case 'issue':
          result = await api.issue(args[0] as number)
          break
        case 'comment':
          result = await api.comment(args[0] as number, args[1] as string)
          break
        case 'setIssueState':
          result = await api.setIssueState(
            args[0] as number,
            args[1] as 'open' | 'closed',
            args[2] as 'completed' | 'not_planned' | undefined,
          )
          break
        case 'checkLog':
          result = await api.checkLog(args[0] as string)
          break
        default:
          throw new Error(`unknown method ${call.method}`)
      }
      results.push({ value: result })
    } catch (error) {
      results.push({
        error: error instanceof Error ? error.message : String(error),
        ...(error instanceof GitHubError ? { status: error.status, reason: error.reason } : {}),
      })
    }
  }
  requests.sort((a, b) => {
    const x = `${a.method} ${a.path}`
    const y = `${b.method} ${b.path}`
    return x < y ? -1 : x > y ? 1 : 0
  })
  scenarios.push({ results, requests })
}
const repositories = input.repositories.map((repository) => {
  try {
    return { value: parseRepository(repository) }
  } catch (error) {
    return { error: (error as Error).message }
  }
})
const links = input.links.map(({ url, web }) => parsePullRequestUrl(new URL(url), web))
const provider = githubProvider({ dataDir: '/unused' })
const { connect: _connect, ...metadata } = provider
process.stdout.write(JSON.stringify({ scenarios, repositories, links, metadata }))
