import { expect, test } from 'vitest'
import { storedProjectSettings } from './project-settings'

test('settings saved before there was anything automatic keep what the user wrote', () => {
  const stored = JSON.stringify({ workspace: { setup: 'make', check: '', shell: 'auto' } })
  expect(storedProjectSettings(stored).workspace).toMatchObject({
    auto: { setup: false, check: true },
    setup: 'make',
  })
  expect(storedProjectSettings(null).workspace.auto).toEqual({ setup: true, check: true })
})
