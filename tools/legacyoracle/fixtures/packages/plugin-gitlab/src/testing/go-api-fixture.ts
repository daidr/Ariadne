import { GitLabApi, GitLabError, plainLog } from '../api'
import {
  GitLabInstances,
  gitlabProvider,
  parseInstance,
  parseMergeRequestUrl,
  parseRepository,
} from '../index'

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
  repositories: { input: string; instance?: string }[]
  instances: string[]
  links: { url: string; instance: string }[]
  logs: string[]
  registered: { instance: string; path: string; token: string }[]
  find: string[]
}
const scenarios = []
for (const scenario of input.scenarios) {
  const requests: { method: string; path: string; body: unknown; authorization: string | null }[] =
    []
  const api = new GitLabApi({
    base: 'http://fixture/api/v4',
    path: 'group/sub/app',
    webUrl: 'https://gitlab.test/group/sub/app',
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
        case 'project':
          result = await api.project()
          break
        case 'mergeRequestFor':
          result = await api.mergeRequestFor(args[0] as string, args[1] as number)
          break
        case 'mergeRequest':
          result = await api.mergeRequest(args[0] as number)
          break
        case 'mergeRequestPreview':
          result = await api.mergeRequestPreview(args[0] as number)
          break
        case 'openMergeRequest':
          result = await api.openMergeRequest(args[0] as Parameters<typeof api.openMergeRequest>[0])
          break
        case 'mergeMergeRequest':
          result = await api.mergeMergeRequest(
            args[0] as number,
            args[1] as Parameters<typeof api.mergeMergeRequest>[1],
          )
          break
        case 'checks':
          result = await api.checks(args[0] as string)
          break
        case 'checkLog':
          result = await api.checkLog(args[0] as string)
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
          result = await api.setIssueState(args[0] as number, args[1] as 'open' | 'closed')
          break
        default:
          throw new Error(`unknown method ${call.method}`)
      }
      results.push({ value: result })
    } catch (error) {
      results.push({
        error: error instanceof Error ? error.message : String(error),
        ...(error instanceof GitLabError ? { status: error.status, reason: error.reason } : {}),
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
const repositories = input.repositories.map(({ input: repository, instance }) => {
  try {
    return { value: parseRepository(repository, instance) }
  } catch (error) {
    return { error: (error as Error).message }
  }
})
const instances = input.instances.map((instance) => {
  try {
    return { value: parseInstance(instance) }
  } catch (error) {
    return { error: (error as Error).message }
  }
})
const links = input.links.map(({ url, instance }) => parseMergeRequestUrl(new URL(url), instance))
const registered = new GitLabInstances({})
for (const project of input.registered)
  registered.add(project.instance, project.path, project.token)
const find = input.find.map((url) => registered.find(new URL(url)))
const provider = gitlabProvider({ dataDir: '/unused' })
const { connect: _connect, secretsFor: _secretsFor, ...metadata } = provider
process.stdout.write(
  JSON.stringify({
    scenarios,
    repositories,
    instances,
    links,
    find,
    metadata,
    logs: input.logs.map(plainLog),
  }),
)
