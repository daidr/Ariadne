import { shikiLanguage } from '@dou-ui/code/shiki'
import { loadShikiLanguage } from '../highlighting/shiki'

export async function extraCodeLanguage(extension: string) {
  const loaded = await loadShikiLanguage(extension)
  return loaded ? shikiLanguage(loaded.engine, loaded.name) : null
}
