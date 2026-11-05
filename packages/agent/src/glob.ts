/**
 * A glob pattern as a regular expression over `/`-separated relative paths: `*` and `?`
 * stay within a directory, `**` crosses directories, `{a,b}` is either, `[abc]` one of.
 * A pattern without `/` matches a file's name at any depth, as `*.ts` would be meant.
 */
export function globToRegExp(pattern: string): RegExp {
  let glob = pattern.trim().replace(/\\/g, '/').replace(/^\.\//, '')
  if (!glob.includes('/')) glob = `**/${glob}`
  let source = ''
  let braces = 0
  for (let i = 0; i < glob.length; i++) {
    const char = glob[i]!
    if (char === '*') {
      if (glob[i + 1] === '*') {
        // `**/` is any number of directories, none included; `**` at the end is anything.
        if (glob[i + 2] === '/') {
          source += '(?:[^/]*/)*'
          i += 2
        } else {
          source += '.*'
          i += 1
        }
      } else source += '[^/]*'
    } else if (char === '?') source += '[^/]'
    else if (char === '{') {
      braces++
      source += '(?:'
    } else if (char === '}' && braces) {
      braces--
      source += ')'
    } else if (char === ',' && braces) source += '|'
    else if (char === '[') {
      const close = glob.indexOf(']', i + 1)
      if (close === -1) source += '\\['
      else {
        const set = glob
          .slice(i + 1, close)
          .replace(/^!/, '^')
          .replace(/\\/g, '\\\\')
        source += `[${set}]`
        i = close
      }
    } else source += char.replace(/[.+^$()|\\]/g, '\\$&')
  }
  source += ')'.repeat(braces)
  return new RegExp(`^${source}$`)
}
