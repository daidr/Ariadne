import type { PluginRecord } from '../api'

export type BuiltinPluginGroupKey = 'models' | 'searches' | 'sources' | 'other'

export interface BuiltinPluginGroup {
  key: BuiltinPluginGroupKey
  plugins: PluginRecord[]
}

const GROUP_ORDER: BuiltinPluginGroupKey[] = ['models', 'searches', 'sources', 'other']

/** Returns every distinct capability kind provided by a plugin. */
export function pluginProvides(plugin: PluginRecord): string[] {
  return [
    ...new Set([
      ...plugin.provides.models,
      ...plugin.provides.contexts,
      ...plugin.provides.dashboards,
      ...plugin.provides.searches,
      ...plugin.provides.links,
    ]),
  ]
}

/** Classifies a built-in plugin into a single display group for Settings -> Plugins. */
export function builtinGroupOf(plugin: PluginRecord): BuiltinPluginGroupKey {
  if (plugin.provides.models.length > 0) return 'models'
  if (plugin.provides.searches.length > 0) return 'searches'
  if (plugin.provides.contexts.length > 0 || plugin.provides.links.length > 0) return 'sources'
  return 'other'
}

export function splitPlugins(records: PluginRecord[]): {
  thirdParty: PluginRecord[]
  builtins: PluginRecord[]
} {
  const thirdParty: PluginRecord[] = []
  const builtins: PluginRecord[] = []
  for (const record of records) {
    if (record.source === 'builtin') {
      builtins.push(record)
    } else {
      thirdParty.push(record)
    }
  }
  return { thirdParty, builtins }
}

export function groupBuiltinPlugins(builtins: PluginRecord[]): BuiltinPluginGroup[] {
  const buckets: Record<BuiltinPluginGroupKey, PluginRecord[]> = {
    models: [],
    searches: [],
    sources: [],
    other: [],
  }
  for (const plugin of builtins) {
    buckets[builtinGroupOf(plugin)].push(plugin)
  }
  return GROUP_ORDER.filter((key) => buckets[key].length > 0).map((key) => ({
    key,
    plugins: buckets[key],
  }))
}

export function affectedConfigs(
  plugin: PluginRecord,
  modelProviders: { name: string; kind: string }[],
  activeSearchProvider: string,
  searchTitles: Record<string, string> = {},
): string[] {
  const names: string[] = []
  for (const provider of modelProviders) {
    if (plugin.provides.models.includes(provider.kind)) {
      names.push(provider.name || provider.kind)
    }
  }
  if (activeSearchProvider && plugin.provides.searches.includes(activeSearchProvider)) {
    names.push(searchTitles[activeSearchProvider] || activeSearchProvider)
  }
  return names
}
