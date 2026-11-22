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
  'makefile',
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
  dotenv: 'ini',
  editorconfig: 'ini',
  env: 'ini',
  gnumakefile: 'makefile',
  golang: 'go',
  gql: 'graphql',
  h: 'c',
  hh: 'cpp',
  hpp: 'cpp',
  htm: 'html',
  js: 'javascript',
  json5: 'jsonc',
  kt: 'kotlin',
  kts: 'kotlin',
  make: 'makefile',
  md: 'markdown',
  mdx: 'markdown',
  mjs: 'javascript',
  mk: 'makefile',
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

const SPECIAL_FILENAMES: Record<string, string> = {
  '.bashrc': 'bash',
  '.bash_profile': 'bash',
  '.editorconfig': 'ini',
  '.gitconfig': 'ini',
  '.npmrc': 'ini',
  '.profile': 'bash',
  '.zprofile': 'bash',
  '.zshrc': 'bash',
  gnumakefile: 'makefile',
  makefile: 'makefile',
}

const EXTENSION_ALIASES: Record<string, string> = {
  mdx: 'markdown',
}

/** Extract a language hint from a file path, handling special filenames, dotfiles and extensions. */
export function languageHintOf(path: string): string | null {
  const name = path.split(/[\\/]/).at(-1)?.trim().toLowerCase() ?? ''
  if (!name) return null
  if (Object.hasOwn(SPECIAL_FILENAMES, name)) return SPECIAL_FILENAMES[name]!
  if (/^dockerfile(?:\.|$)/.test(name)) return 'dockerfile'
  if (/^makefile(?:\.|$)/.test(name)) return 'makefile'
  if (/^\.env(?:\.|$)/.test(name)) return 'ini'
  const dot = name.lastIndexOf('.')
  if (dot <= 0 || dot === name.length - 1) return null
  const ext = name.slice(dot + 1)
  return EXTENSION_ALIASES[ext] ?? ext
}
