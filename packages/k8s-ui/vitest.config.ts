import { readdirSync, readFileSync } from 'fs'
import path from 'path'
import { configDefaults, defineConfig } from 'vitest/config'

// Most test files run without isolation: a worker reuses its module registry
// across files (Vitest still gives each environment its own workers), which cuts
// the suite from ~95s to ~35s. Without isolation Vitest resets neither the mock
// registry nor the module cache between files, so a file that mocks a module
// would miss it when an earlier file loaded it and leak the mock into later
// files. Those files run isolated, picked from their source.
function testFiles(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    if (entry.name.startsWith('.') || entry.name === 'node_modules' || entry.name === 'dist') return []
    const file = path.join(dir, entry.name)
    if (entry.isDirectory()) return testFiles(file)
    return /\.(test|spec)\.[cm]?[jt]sx?$/.test(entry.name) ? [file] : []
  })
}
const mockingTests = testFiles(__dirname)
  .filter((file) => /\bvi\s*\.\s*(mock|doMock)\s*\(/.test(readFileSync(file, 'utf8')))
  .map((file) => path.relative(__dirname, file).split(path.sep).join('/'))

export default defineConfig({
  test: {
    fsModuleCache: true,
    // Worktrees symlink node_modules to one checkout, so the default cache
    // location would be shared by every worktree.
    fsModuleCachePath: path.resolve(__dirname, '.vitest-cache'),
    projects: [
      { extends: true, test: { name: 'shared', isolate: false, exclude: [...configDefaults.exclude, ...mockingTests] } },
      { extends: true, test: { name: 'isolated', include: mockingTests } },
    ],
  },
})
