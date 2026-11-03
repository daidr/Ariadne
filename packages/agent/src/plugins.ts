import { join } from 'node:path'
import type {
  ContextProvider,
  Dispose,
  ModelProvider,
  Plugin,
  PluginContext,
} from '@ariadne/plugin-sdk'

const register =
  <T extends { kind: string }>(map: Map<string, T>) =>
  (provider: T): Dispose => {
    if (map.has(provider.kind)) throw new Error(`${provider.kind} is already registered`)
    map.set(provider.kind, provider)
    return () => {
      map.delete(provider.kind)
    }
  }

/** Everything plugins registered, by kind. */
export class PluginHost {
  readonly models = new Map<string, ModelProvider<never>>()
  readonly contexts = new Map<string, ContextProvider<never>>()
  readonly plugins: Plugin[] = []
  private readonly disposers: Dispose[] = []

  /** `dataRoot` holds one data directory per plugin, named by its id. */
  constructor(private readonly dataRoot: string) {}

  async load(plugin: Plugin): Promise<void> {
    if (this.plugins.some((loaded) => loaded.id === plugin.id)) {
      throw new Error(`plugin ${plugin.id} is already loaded`)
    }
    const ctx: PluginContext = {
      dataDir: join(this.dataRoot, plugin.id),
      registerModelProvider: register(this.models),
      registerContextProvider: register(this.contexts),
    }
    const dispose = await plugin.setup(ctx)
    if (dispose) this.disposers.push(dispose)
    this.plugins.push(plugin)
  }

  /** Load a plugin from a local path; its default export is the plugin. */
  async loadFromPath(path: string): Promise<void> {
    const module = await import(path)
    const plugin = module.default as Plugin | undefined
    if (!plugin?.id || typeof plugin.setup !== 'function') {
      throw new Error(`${path} does not export a plugin as default`)
    }
    await this.load(plugin)
  }

  async unloadAll(): Promise<void> {
    for (const dispose of this.disposers.splice(0).toReversed()) await dispose()
    this.plugins.length = 0
  }
}
