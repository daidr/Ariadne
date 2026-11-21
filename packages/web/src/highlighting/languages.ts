/** Canonical language keys supported by the shared Shiki highlighter. */
export const SHIKI_LANGUAGES = [
  'bash',
  'c',
  'cpp',
  'csharp',
  'css',
  'diff',
  'dockerfile',
  'go',
  'graphql',
  'html',
  'ini',
  'java',
  'javascript',
  'json',
  'jsonc',
  'jsx',
  'kotlin',
  'lua',
  'markdown',
  'php',
  'powershell',
  'proto',
  'python',
  'ruby',
  'rust',
  'scss',
  'sql',
  'swift',
  'toml',
  'tsx',
  'typescript',
  'vue',
  'xml',
  'yaml',
] as const

export type ShikiLanguageKey = (typeof SHIKI_LANGUAGES)[number]

const supported = new Set<string>(SHIKI_LANGUAGES)

const aliases: Record<string, ShikiLanguageKey> = {
  'c#': 'csharp',
  'c++': 'cpp',
  cc: 'cpp',
  cfg: 'ini',
  cjs: 'javascript',
  conf: 'ini',
  cs: 'csharp',
  cts: 'typescript',
  cxx: 'cpp',
  docker: 'dockerfile',
  env: 'ini',
  golang: 'go',
  gql: 'graphql',
  hh: 'cpp',
  hpp: 'cpp',
  htm: 'html',
  js: 'javascript',
  json5: 'jsonc',
  kt: 'kotlin',
  kts: 'kotlin',
  md: 'markdown',
  mjs: 'javascript',
  mts: 'typescript',
  patch: 'diff',
  properties: 'ini',
  protobuf: 'proto',
  ps1: 'powershell',
  pwsh: 'powershell',
  py: 'python',
  rb: 'ruby',
  rs: 'rust',
  sh: 'bash',
  shell: 'bash',
  shellscript: 'bash',
  svg: 'html',
  ts: 'typescript',
  yml: 'yaml',
  zsh: 'bash',
}

/** Normalize a fence language tag or file extension to a supported Shiki grammar key. */
export function resolveShikiLanguage(raw: string): ShikiLanguageKey | null {
  const key = raw.trim().toLowerCase()
  if (!key) return null
  if (Object.hasOwn(aliases, key)) return aliases[key]!
  return supported.has(key) ? (key as ShikiLanguageKey) : null
}
