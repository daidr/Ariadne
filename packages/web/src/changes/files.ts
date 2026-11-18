import type { ChangedFile } from '@ariadne/plugin-sdk'

/** A row of the changed-file tree: a folder (its path compacted) or a file. */
export type FileNode = {
  key: string
  label: string
  children?: FileNode[]
  file?: ChangedFile
}

const DIR = 'dir:'

/**
 * The changed files as a tree of folders, folders first and by name. A folder with only
 * one folder inside is shown as one row (`src/components`), as code review tools do.
 */
export function fileTree(files: ChangedFile[]): FileNode[] {
  const root: FileNode = { key: DIR, label: '', children: [] }
  for (const file of files) {
    const parts = file.path.split('/')
    let node = root
    for (let i = 0; i < parts.length - 1; i++) {
      const key = `${DIR}${parts.slice(0, i + 1).join('/')}`
      let next = node.children!.find((child) => child.key === key)
      if (!next) {
        next = { key, label: parts[i]!, children: [] }
        node.children!.push(next)
      }
      node = next
    }
    node.children!.push({ key: file.path, label: parts.at(-1)!, file })
  }
  return compact(root).children ?? []
}

function compact(node: FileNode): FileNode {
  if (!node.children) return node
  let children = node.children.map(compact)
  children = children.map((child) => {
    // A folder holding just one folder reads as one path.
    while (child.children?.length === 1 && child.children[0]!.children) {
      const only = child.children[0]!
      child = { ...only, label: `${child.label}/${only.label}` }
    }
    return child
  })
  children.sort((a, b) =>
    !!a.children === !!b.children ? a.label.localeCompare(b.label) : a.children ? -1 : 1,
  )
  return { ...node, children }
}

/** Keys of every folder, to open them all. */
export function folderKeys(nodes: FileNode[]): string[] {
  return nodes.flatMap((node) => (node.children ? [node.key, ...folderKeys(node.children)] : []))
}

/** The files in the order the tree shows them. */
export function orderedFiles(nodes: FileNode[]): ChangedFile[] {
  return nodes.flatMap((node) => (node.file ? [node.file] : orderedFiles(node.children ?? [])))
}

/** Lines added and removed over all files; binary files count for nothing. */
export function lineTotals(files: ChangedFile[]): { added: number; removed: number } {
  let added = 0
  let removed = 0
  for (const file of files) {
    added += file.added ?? 0
    removed += file.removed ?? 0
  }
  return { added, removed }
}
