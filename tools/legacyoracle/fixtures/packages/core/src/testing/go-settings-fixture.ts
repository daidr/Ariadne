/** Legacy validator oracle; this program is used only by migration tests. */
import {
  branchSlug,
  parseProjectSettings,
  storedProjectSettings,
  validBranchPrefix,
} from '../project-settings'
import { DomainError } from '../errors'

const input = (await Bun.stdin.json()) as {
  mode: 'parse' | 'stored' | 'slug' | 'prefix'
  input: unknown
  tools?: string[]
}[]
console.info(
  JSON.stringify(
    input.map((test) => {
      try {
        const value =
          test.mode === 'parse'
            ? parseProjectSettings(test.input, test.tools ?? [])
            : test.mode === 'stored'
              ? storedProjectSettings(test.input as string | null)
              : test.mode === 'slug'
                ? branchSlug(test.input)
                : validBranchPrefix(test.input as string)
        return { value }
      } catch (error) {
        return {
          error: error instanceof DomainError ? error.toLocalized() : { message: String(error) },
        }
      }
    }),
  ),
)
