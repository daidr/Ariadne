import fs from 'node:fs'
import { createRequire } from 'node:module'
import path from 'node:path'
import type { Plugin } from 'vite'

const IMPORT_DOU_UI_RE = /import\s*\{([^}]+)\}\s*from\s*['"]dou-ui['"]/g

function buildDouUiStyleMap(root: string): Map<string, string> {
  const req = createRequire(path.join(root, 'package.json'))
  const douPkgPath = req.resolve('dou-ui/package.json')
  const douDir = path.dirname(douPkgPath)
  const indexCode = fs.readFileSync(path.join(douDir, 'dist', 'index.js'), 'utf8')

  const symbolToDir = new Map<string, string>()
  const re = /import\s*\{([^}]+)\}\s*from\s*["']\.\/components\/([^/]+)\/[^"']+["']/g
  for (const match of indexCode.matchAll(re)) {
    const dir = match[2]
    if (!dir) continue
    if (!fs.existsSync(path.join(douDir, 'dist', 'components', dir, 'style.js'))) continue
    for (const part of match[1]!.split(',')) {
      const cleaned = part.trim()
      if (!cleaned) continue
      const exported = cleaned
        .split(/\s+as\s+/)
        .pop()
        ?.trim()
      if (exported) symbolToDir.set(exported, dir)
    }
  }
  return symbolToDir
}

/**
 * Replaces the full `dou-ui/style.css` (~500 kB) with DouUI's official per-component
 * `dou-ui/components/<name>/style` imports during production builds, so unused and
 * lazy-route component stylesheets stay out of the initial load.
 */
export function douUiOnDemandStylesPlugin(): Plugin {
  let root = ''
  let symbolToDir = new Map<string, string>()

  return {
    name: 'ariadne-dou-ui-on-demand-styles',
    apply: 'build',
    configResolved(config) {
      root = config.root
    },
    buildStart() {
      if (symbolToDir.size === 0) {
        symbolToDir = buildDouUiStyleMap(root)
      }
    },
    resolveId: {
      order: 'pre',
      async handler(source, importer, options) {
        if (source !== 'dou-ui/style.css') return null
        return this.resolve('dou-ui/components/config-provider/style', importer, {
          ...options,
          skipSelf: true,
        })
      },
    },
    transform(code, id) {
      if (!code.includes("'dou-ui'") && !code.includes('"dou-ui"')) return null
      const normalized = id.replace(/\\/g, '/')
      if (normalized.includes('/dou-ui/dist/')) return null
      if (
        normalized.includes('type=style') ||
        /\.(?:css|scss|less|json)(?:\?|$)/.test(normalized)
      ) {
        return null
      }

      const styleDirs = new Set<string>()
      for (const match of code.matchAll(IMPORT_DOU_UI_RE)) {
        for (const raw of match[1]!.split(',')) {
          const trimmed = raw.trim()
          if (!trimmed || trimmed.startsWith('type ')) continue
          const imported = trimmed.split(/\s+as\s+/)[0]?.trim()
          if (!imported) continue
          const dir = symbolToDir.get(imported)
          if (dir) styleDirs.add(dir)
        }
      }
      if (styleDirs.size === 0) return null

      const imports = [...styleDirs]
        .toSorted()
        .map((dir) => `import 'dou-ui/components/${dir}/style';`)
        .join('\n')
      return {
        code: `${imports}\n${code}`,
        map: null,
      }
    },
  }
}
