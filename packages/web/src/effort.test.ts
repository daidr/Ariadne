import { expect, test } from 'vitest'
import { effortsOf, effortWithin } from './effort'

test('a model whose efforts nobody told takes the common ones, as the server does', () => {
  expect(effortsOf(null)).toEqual(['low', 'medium', 'high'])
  expect(effortsOf(['low', 'max'])).toEqual(['low', 'max'])
  expect(effortsOf([])).toEqual([])
  // Picked for a model that took it, then the model changed to one nobody told about.
  expect(effortWithin('xhigh', effortsOf(null))).toBe('high')
  expect(effortWithin('max', effortsOf(['low', 'medium', 'high', 'max']))).toBe('max')
})

test('an effort a model does not take becomes the nearest it does', () => {
  expect(effortWithin('xhigh', ['low', 'high', 'max'])).toBe('high')
  expect(effortWithin('low', ['medium', 'high'])).toBe('medium')
  expect(effortWithin('high', [])).toBeNull()
})
