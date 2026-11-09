/**
 * How a model is named where the user picks or sees one: by its own name. The provider's
 * name joins only when two providers have a model of that name, so the label still tells
 * them apart.
 */
export function modelLabels<M extends { id: string; provider: string; model: string }>(
  models: readonly M[],
  providers: readonly { id: string; name: string }[],
): { id: string; label: string }[] {
  const names = models.map((model) => model.model.trim())
  return models.map((model, index) => {
    const name = names[index]!
    if (names.indexOf(name) === names.lastIndexOf(name)) return { id: model.id, label: name }
    const provider = providers.find((p) => p.id === model.provider)?.name || model.provider
    return { id: model.id, label: `${provider} / ${name}` }
  })
}
