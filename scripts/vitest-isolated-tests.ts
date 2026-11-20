import { readdirSync, readFileSync } from 'fs'
import path from 'path'

// Test files run without isolation by default: a worker reuses its module
// registry across files (Vitest still gives each environment its own workers),
// which skips re-importing the shared module graph for every file. Without
// isolation Vitest resets neither the mock registry nor the module cache between
// files, so a file that mocks a module would miss it when an earlier file loaded
// it and leak the mock into later files, and a file that resets modules leaves
// its fresh instances (with whatever state it set) for later files. Those files
// run isolated; this returns them, relative to `root`.
export function isolatedTestFiles(root: string): string[] {
  const walk = (dir: string): string[] =>
    readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
      if (entry.name.startsWith('.') || entry.name === 'node_modules' || entry.name === 'dist') return []
      const file = path.join(dir, entry.name)
      if (entry.isDirectory()) return walk(file)
      return /\.(test|spec)\.[cm]?[jt]sx?$/.test(entry.name) ? [file] : []
    })
  return walk(root)
    .filter((file) => /\bvi\s*\.\s*(mock|doMock|resetModules)\s*\(/.test(readFileSync(file, 'utf8')))
    .map((file) => path.relative(root, file).split(path.sep).join('/'))
}
