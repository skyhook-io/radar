import path from 'path'
import { configDefaults, defineConfig, mergeConfig } from 'vitest/config'
import { isolatedTestFiles } from '../scripts/vitest-isolated-tests'
import viteConfig from './vite.config'

// Unit tests live under src; e2e/ holds Playwright specs.
const isolated = isolatedTestFiles(__dirname).filter((file) => file.startsWith('src/'))

export default mergeConfig(
  viteConfig,
  defineConfig({
    test: {
      projects: [
        {
          extends: true,
          test: {
            name: 'shared',
            isolate: false,
            setupFiles: [path.resolve(__dirname, '../scripts/vitest-shared-setup.ts')],
            include: ['src/**/*.{test,spec}.?(c|m)[jt]s?(x)'],
            exclude: [...configDefaults.exclude, ...isolated],
          },
        },
        { extends: true, test: { name: 'isolated', include: isolated } },
      ],
    },
  }),
)
