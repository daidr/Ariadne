import { describe, expect, it } from 'vitest'
import { modelLabels } from './model-label'

const providers = [
  { id: 'p1', name: 'First' },
  { id: 'p2', name: '' },
]

describe('modelLabels', () => {
  it('names a model by its own name', () => {
    const models = [
      { id: 'a', provider: 'p1', model: ' fast ' },
      { id: 'b', provider: 'p2', model: 'deep' },
    ]
    expect(modelLabels(models, providers)).toEqual([
      { id: 'a', label: 'fast' },
      { id: 'b', label: 'deep' },
    ])
  })

  it('adds the provider when two providers have a model of that name', () => {
    const models = [
      { id: 'a', provider: 'p1', model: 'fast' },
      { id: 'b', provider: 'p2', model: 'fast' },
      { id: 'c', provider: 'p1', model: 'deep' },
    ]
    expect(modelLabels(models, providers)).toEqual([
      { id: 'a', label: 'First / fast' },
      { id: 'b', label: 'p2 / fast' },
      { id: 'c', label: 'deep' },
    ])
  })
})
