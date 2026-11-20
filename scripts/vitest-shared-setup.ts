import { afterAll, vi } from 'vitest'

// Files in a shared (non-isolated) project hand their globals to the next file in
// the worker. Undo the global state a file can set through Vitest once its last
// test finishes; files that stub at load time keep their stubs for all their tests.
afterAll(() => {
  vi.unstubAllGlobals()
  vi.unstubAllEnvs()
  vi.useRealTimers()
})
