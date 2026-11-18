import { checkArtifactContent, emptyContent, previewOf } from '../artifact-content'
import { DomainError } from '../errors'
import type { ArtifactKind } from '../events'

const cases = (await Bun.stdin.json()) as { kind: ArtifactKind; content: string }[]
console.info(
  JSON.stringify(
    cases.map(({ kind, content }) => {
      let error = null
      try {
        checkArtifactContent(kind, content)
      } catch (caught) {
        error = caught instanceof DomainError ? caught.toLocalized() : { message: String(caught) }
      }
      return { error, preview: previewOf(kind, content), empty: emptyContent(kind) }
    }),
  ),
)
