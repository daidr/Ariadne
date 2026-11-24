import { defaultLanguages, type CodeLanguage } from '@dou-ui/editor'
import { resolveShikiLanguage } from '../highlighting/languages'
import { shikiWorkerPool } from '../highlighting/shiki-worker-pool'

const extraLanguages: CodeLanguage[] = [
  { value: 'dockerfile', label: 'Dockerfile' },
  { value: 'graphql', label: 'GraphQL' },
  { value: 'ini', label: 'INI' },
  { value: 'jsonc', label: 'JSONC' },
  { value: 'lua', label: 'Lua' },
  { value: 'powershell', label: 'PowerShell' },
  { value: 'proto', label: 'Protobuf' },
  { value: 'toml', label: 'TOML' },
]

/** Languages offered in the document code block picker, sorted by display label. */
export const documentCodeLanguages: CodeLanguage[] = [
  ...defaultLanguages,
  ...extraLanguages,
].toSorted((a, b) => a.label.localeCompare(b.label))

/**
 * Lazily highlight a document code block with Shiki dual themes (`--shiki-light` / `--shiki-dark`)
 * via the shared Web Worker pool. Plain text or unsupported languages return early without
 * starting workers or loading Shiki.
 */
export async function highlightDocumentCode(code: string, lang: string): Promise<string> {
  if (!code || !resolveShikiLanguage(lang)) return ''
  return shikiWorkerPool.highlight(code, lang)
}
