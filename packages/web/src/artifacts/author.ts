import { t } from '../i18n'
import { state } from '../store'

/** Who wrote an artifact version: the user, the coordinator, or a thread by its title. */
export function authorName(author: string): string {
  if (author === 'user') return t('artifact.by.user')
  if (author === state.detail?.project.coordinatorId) return t('artifact.by.coordinator')
  return state.detail?.threads.find((thread) => thread.id === author)?.title ?? author
}
