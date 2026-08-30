import type { FileNode } from '../../types'

/** Recursively filter a FileNode tree by name substring match. */
export function filterTree(node: FileNode, query: string): FileNode | null {
  if (node.name.toLowerCase().includes(query)) {
    return node
  }

  if (node.type === 'dir' && node.children) {
    const filteredChildren = node.children
      .map((child) => filterTree(child, query))
      .filter((child): child is FileNode => child !== null)

    if (filteredChildren.length > 0) {
      return { ...node, children: filteredChildren }
    }
  }

  return null
}
