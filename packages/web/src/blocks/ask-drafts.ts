import type { FormValue } from '@ariadne/core'
import type { RadioValue } from 'dou-ui'
import type { InjectionKey } from 'vue'

export interface AskDraft {
  values: Record<string, FormValue>
  scope: RadioValue
}

// A virtual chat row may leave the DOM while its question is still open.
export const askDraftsKey: InjectionKey<Map<string, AskDraft>> = Symbol('ask drafts')
