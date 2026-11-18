import type { DashboardSource } from '@ariadne/plugin-sdk'
import { checkSourceSettings, fetchDashboardData } from '../dashboard-sources'

const input = (await Bun.stdin.json()) as {
  source: Omit<DashboardSource, 'fetch'>
  settings: unknown[]
  fetched: Record<string, unknown>
}
console.info(
  JSON.stringify({
    checked: input.settings.map((settings) =>
      checkSourceSettings(input.source as DashboardSource<never>, settings),
    ),
    fetched: await fetchDashboardData(
      { ...input.source, fetch: async () => input.fetched } as DashboardSource<never>,
      {},
      { projectId: 'prj_a' },
    ),
  }),
)
