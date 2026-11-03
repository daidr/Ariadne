import type { LocalizedMessage, MessageKey, MessageParams } from '@ariadne/i18n'

/** An error whose message the client translates. */
export class DomainError extends Error {
  readonly code: MessageKey
  readonly params: MessageParams | undefined

  constructor(code: MessageKey, params?: MessageParams) {
    super(code)
    this.name = 'DomainError'
    this.code = code
    this.params = params
  }

  toLocalized(): LocalizedMessage {
    return this.params ? { key: this.code, params: this.params } : { key: this.code }
  }
}
