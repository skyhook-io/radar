import path from 'path'
import { configDefaults, defineConfig } from 'vitest/config'
import { isolatedTestFiles } from '../../scripts/vitest-isolated-tests'

const isolated = isolatedTestFiles(__dirname)

export default defineConfig({
  test: {
    fsModuleCache: true,
    // Worktrees symlink node_modules to one checkout, so the default cache
    // location would be shared by every worktree.
    fsModuleCachePath: path.resolve(__dirname, '.vitest-cache'),
    projects: [
      {
        extends: true,
        test: {
          name: 'shared',
          isolate: false,
          setupFiles: [path.resolve(__dirname, '../../scripts/vitest-shared-setup.ts')],
          exclude: [...configDefaults.exclude, ...isolated],
        },
      },
      { extends: true, test: { name: 'isolated', include: isolated } },
    ],
  },
})
