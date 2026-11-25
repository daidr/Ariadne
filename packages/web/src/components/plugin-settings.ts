import type { MessageKey } from '@ariadne/i18n'
import type { PluginRecord, PluginUsage } from '../api'
import { t } from '../i18n'

export type PluginCapability =
  'models' | 'searches' | 'contexts' | 'links' | 'dashboards' | 'notifiers'

const CAPABILITY_ORDER: PluginCapability[] = [
  'models',
  'searches',
  'contexts',
  'links',
  'dashboards',
  'notifiers',
]

const ROLE_KEYS: Record<string, MessageKey> = {
  thread: 'plugins.usage.role.thread',
  coordinator: 'plugins.usage.role.coordinator',
  triage: 'plugins.usage.role.triage',
  research: 'plugins.usage.role.research',
}

/** Returns human-facing capability categories provided by a plugin, in display order. */
export function pluginCapabilities(plugin: PluginRecord): PluginCapability[] {
  return CAPABILITY_ORDER.filter((capability) => plugin.provides[capability].length > 0)
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

export function hasPluginUsage(plugin: PluginRecord): boolean {
  const usage = plugin.usage
  if (!usage) return false
  return (
    usage.modelProviders.length > 0 ||
    usage.searches.length > 0 ||
    usage.notifiers.length > 0 ||
    usage.projects.length > 0
  )
}

/** Formats active usage of a plugin into a concise localized summary for rows and confirmations. */
export function formatPluginUsage(usage?: PluginUsage): string {
  if (!usage) return ''
  const sep = t('plugins.listSeparator')
  const parts: string[] = []

  if (usage.modelProviders.length > 0) {
    const names = usage.modelProviders.join(sep)
    if (usage.modelRoles.length > 0) {
      const roles = usage.modelRoles.map((role) => (ROLE_KEYS[role] ? t(ROLE_KEYS[role]) : role))
      parts.push(
        t('plugins.usage.modelProvidersWithRoles', {
          names,
          roles: roles.join(sep),
        }),
      )
    } else {
      parts.push(t('plugins.usage.modelProviders', { names }))
    }
  }

  if (usage.searches.length > 0) {
    parts.push(t('plugins.usage.search'))
  }

  if (usage.notifiers.length > 0) {
    parts.push(t('plugins.usage.notifiers', { names: usage.notifiers.join(sep) }))
  }

  if (usage.projects.length === 1) {
    parts.push(t('plugins.usage.project', { name: usage.projects[0]! }))
  } else if (usage.projects.length > 1 && usage.projects.length <= 3) {
    parts.push(t('plugins.usage.projects', { names: usage.projects.join(sep) }))
  } else if (usage.projects.length > 3) {
    parts.push(
      t('plugins.usage.projectsMore', {
        names: usage.projects.slice(0, 2).join(sep),
        count: usage.projects.length,
      }),
    )
  }

  return parts.join(t('plugins.partSeparator'))
}
