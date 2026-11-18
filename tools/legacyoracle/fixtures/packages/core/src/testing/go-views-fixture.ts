/** Read-model comparison against the existing domain facade. */
import { Ariadne } from '../ariadne'

const input = (await Bun.stdin.json()) as { sql: string; now: number }[]
let now = 0
const core = new Ariadne({ path: ':memory:', now: () => now })
try {
  const snapshots = input.map((step) => {
    now = step.now
    core.store.db.exec(step.sql)
    return { projects: core.listProjects(), detail: core.getProjectDetail('prj_a') }
  })
  console.info(JSON.stringify(snapshots))
} finally {
  core.close()
}
