import type { CodeLanguage } from '@dou-ui/code'
import { shikiLanguage } from '@dou-ui/code/shiki'
import { loadShikiLanguage } from './shiki'

export async function loadShikiCodeLanguage(language: string): Promise<CodeLanguage | null> {
  const loaded = await loadShikiLanguage(language)
  return loaded ? shikiLanguage(loaded.engine, loaded.name) : null
}
