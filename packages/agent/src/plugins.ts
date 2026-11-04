import { existsSync, readFileSync, statSync } from 'node:fs'
import { homedir } from 'node:os'
import { isAbsolute, join, resolve } from 'node:path'
import type {
  ContextProvider,
  DashboardSource,
  Dispose,
  LocalizedText,
  ModelProvider,
  Plugin,
  PluginContext,
} from '@ariadne/plugin-sdk'
import { noSandbox, type Sandbox } from './sandbox'

/** Where a plugin came from: shipped with Ariadne, or a path on this machine. */
export type PluginSource = 'builtin' | 'path'

/** A plugin as the settings page shows it, loaded or not. */
export interface PluginRecord {
  /** The plugin's id; `null` when loading failed before the plugin was read. */
  id: string | null
  name: LocalizedText | null
  source: PluginSource
  /** The path the user gave, for plugins loaded from a path. */
  path: string | null
  status: 'loaded' | 'failed'
  error: string | null
  /** The kinds it registered. */
  provides: { models: string[]; contexts: string[]; dashboards: string[] }
}

interface Loaded {
  record: PluginRecord
  disposers: Dispose[]
}

/**
 * The file a plugin path points at: the file itself, or for a directory its
 * `package.json` entry (`exports["."]`, then `module`, then `main`) or an `index` file.
 * `~/` stands for the home directory; anything else must be absolute.
 */
export function resolvePluginPath(input: string): string {
  const path = input.startsWith('~/') ? join(homedir(), input.slice(2)) : input
  if (!isAbsolute(path)) throw new Error(`${input} is not an absolute path`)
  if (!existsSync(path)) throw new Error(`${input} does not exist`)
  if (!statSync(path).isDirectory()) return path
  const manifest = join(path, 'package.json')
  if (existsSync(manifest)) {
    const pkg = JSON.parse(readFileSync(manifest, 'utf8')) as {
      exports?: unknown
      module?: unknown
      main?: unknown
    }
    const root =
      typeof pkg.exports === 'string'
        ? pkg.exports
        : (pkg.exports as Record<string, unknown> | undefined)?.['.']
    const entry =
      typeof root === 'string'
        ? root
        : typeof root === 'object' && root !== null
          ? ((root as Record<string, unknown>).bun ??
            (root as Record<string, unknown>).import ??
            (root as Record<string, unknown>).default)
          : undefined
    for (const candidate of [entry, pkg.module, pkg.main]) {
      if (typeof candidate === 'string') return resolve(path, candidate)
    }
  }
  for (const name of ['index.ts', 'index.js', 'index.mjs']) {
    if (existsSync(join(path, name))) return join(path, name)
  }
  throw new Error(`${input} has no package.json entry or index file`)
}

/** Everything plugins registered, by kind. */
export class PluginHost {
  readonly models = new Map<string, ModelProvider<never>>()
  readonly contexts = new Map<string, ContextProvider<never>>()
  readonly dashboards = new Map<string, DashboardSource<never>>()
  private readonly loaded: Loaded[] = []
  /** Paths that failed to load, kept so the settings page can say why. */
  private readonly failed: PluginRecord[] = []

  /**
   * `dataRoot` holds one data directory per plugin, named by its id. `sandbox` is what
   * plugins run thread commands in.
   */
  constructor(
    private readonly dataRoot: string,
    readonly sandbox: Sandbox = noSandbox,
  ) {}

  get plugins(): PluginRecord[] {
    return this.loaded.map((entry) => entry.record)
  }

  /** Every plugin, loaded first, then the paths that failed. */
  records(): PluginRecord[] {
    return [...this.plugins, ...this.failed]
  }

  /**
   * Run a plugin's setup. A setup that throws, or registers a kind another plugin has,
   * leaves nothing registered.
   */
  async load(plugin: Plugin, path: string | null = null): Promise<void> {
    if (this.loaded.some((entry) => entry.record.id === plugin.id)) {
      throw new Error(`plugin ${plugin.id} is already loaded`)
    }
    const disposers: Dispose[] = []
    const provides: PluginRecord['provides'] = { models: [], contexts: [], dashboards: [] }
    const register =
      <T extends { kind: string }>(map: Map<string, T>, list: string[]) =>
      (provider: T): Dispose => {
        if (map.has(provider.kind)) throw new Error(`${provider.kind} is already registered`)
        map.set(provider.kind, provider)
        list.push(provider.kind)
        const dispose = () => {
          if (map.get(provider.kind) === provider) map.delete(provider.kind)
        }
        disposers.push(dispose)
        return dispose
      }
    const ctx: PluginContext = {
      dataDir: join(this.dataRoot, plugin.id),
      sandbox: this.sandbox,
      registerModelProvider: register(this.models, provides.models),
      registerContextProvider: register(this.contexts, provides.contexts),
      registerDashboardSource: register(this.dashboards, provides.dashboards),
    }
    try {
      const dispose = await plugin.setup(ctx)
      if (dispose) disposers.push(dispose)
    } catch (error) {
      await runAll(disposers)
      throw error
    }
    this.loaded.push({
      record: {
        id: plugin.id,
        name: plugin.name,
        source: path === null ? 'builtin' : 'path',
        path,
        status: 'loaded',
        error: null,
        provides,
      },
      disposers,
    })
  }

  /**
   * Load a plugin from a path on this machine; its default export is the plugin. A
   * failure is kept for the settings page and thrown.
   */
  async loadFromPath(path: string): Promise<void> {
    this.forgetFailure(path)
    try {
      const module = (await import(resolvePluginPath(path))) as { default?: Plugin }
      const plugin = module.default
      if (!plugin?.id || typeof plugin.setup !== 'function') {
        throw new Error(`${path} does not export a plugin as default`)
      }
      await this.load(plugin, path)
    } catch (error) {
      this.failed.push({
        id: null,
        name: null,
        source: 'path',
        path,
        status: 'failed',
        error: error instanceof Error ? error.message : String(error),
        provides: { models: [], contexts: [], dashboards: [] },
      })
      throw error
    }
  }

  /**
   * Unload the plugin loaded from `path`, or forget why it failed. What it registered
   * goes at once; sources already connected keep working until the server restarts.
   */
  async unloadPath(path: string): Promise<void> {
    this.forgetFailure(path)
    const index = this.loaded.findIndex((entry) => entry.record.path === path)
    if (index < 0) return
    const [entry] = this.loaded.splice(index, 1)
    if (entry) await runAll(entry.disposers)
  }

  async unloadAll(): Promise<void> {
    for (const entry of this.loaded.splice(0).toReversed()) await runAll(entry.disposers)
    this.failed.length = 0
  }

  private forgetFailure(path: string): void {
    const index = this.failed.findIndex((record) => record.path === path)
    if (index >= 0) this.failed.splice(index, 1)
  }
}

/** Run disposers newest first; one failing does not keep the others from running. */
async function runAll(disposers: Dispose[]): Promise<void> {
  for (const dispose of disposers.splice(0).toReversed()) {
    try {
      await dispose()
    } catch (error) {
      console.error('插件卸载出错', error)
    }
  }
}
