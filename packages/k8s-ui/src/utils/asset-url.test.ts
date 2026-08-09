import { describe, it, expect, afterEach } from 'vitest'
import { assetUrl } from './asset-url'

type RuntimeGlobal = typeof globalThis & {
  __RADAR_RUNTIME_CONFIG__?: { assetBase?: string }
}

function setAssetBase(assetBase: string) {
  ;(globalThis as RuntimeGlobal).__RADAR_RUNTIME_CONFIG__ = { assetBase }
}

/**
 * Simulates a Worker scope, where `window` is absent and `location` is the
 * worker script's own URL. jsdom defines `window`, so it has to be removed.
 */
function asWorkerAt(pathname: string, run: () => void) {
  const realWindow = globalThis.window
  // @ts-expect-error — deliberately emulating a scope without `window`
  delete globalThis.window
  const realLocation = globalThis.location
  Object.defineProperty(globalThis, 'location', {
    value: { pathname },
    configurable: true,
    writable: true,
  })
  try {
    run()
  } finally {
    Object.defineProperty(globalThis, 'location', {
      value: realLocation,
      configurable: true,
      writable: true,
    })
    globalThis.window = realWindow
  }
}

afterEach(() => {
  delete (globalThis as RuntimeGlobal).__RADAR_RUNTIME_CONFIG__
})

describe('assetUrl at the root (no base path)', () => {
  it('leaves absolute paths untouched', () => {
    expect(assetUrl('/images/radar/radar-icon.svg')).toBe('/images/radar/radar-icon.svg')
  })

  it('makes Vite-relative asset paths absolute', () => {
    expect(assetUrl('./assets/index-abc.js')).toBe('/assets/index-abc.js')
    expect(assetUrl('assets/index-abc.js')).toBe('/assets/index-abc.js')
  })

  it('unwraps the webpack/Next StaticImageData shape', () => {
    expect(assetUrl({ src: '/_next/static/x.png' })).toBe('/_next/static/x.png')
  })

  // An app route may contain an "/assets/" segment (e.g. a namespace named
  // "assets"). That must not be mistaken for a base path.
  it('ignores an /assets/ segment in the current app route', () => {
    expect(assetUrl('/images/radar/radar-icon.svg')).toBe('/images/radar/radar-icon.svg')
  })
})

describe('assetUrl under a base path', () => {
  it('prefixes absolute and relative asset paths', () => {
    setAssetBase('/radar')
    expect(assetUrl('/images/radar/radar-icon.svg')).toBe('/radar/images/radar/radar-icon.svg')
    expect(assetUrl('./assets/index-abc.js')).toBe('/radar/assets/index-abc.js')
    expect(assetUrl('assets/index-abc.js')).toBe('/radar/assets/index-abc.js')
  })

  it('is idempotent for already-prefixed paths', () => {
    setAssetBase('/radar')
    expect(assetUrl('/radar/assets/index-abc.js')).toBe('/radar/assets/index-abc.js')
  })

  it('leaves protocol-relative URLs alone', () => {
    setAssetBase('/radar')
    expect(assetUrl('//cdn.example.com/x.png')).toBe('//cdn.example.com/x.png')
  })

  it('handles a nested base path', () => {
    setAssetBase('/tools/radar')
    expect(assetUrl('/favicon.svg')).toBe('/tools/radar/favicon.svg')
  })
})

describe('assetUrl inside a Worker', () => {
  it('recovers the base path from the worker script URL', () => {
    asWorkerAt('/radar/assets/layout.worker-abc.js', () => {
      expect(assetUrl('/images/x.svg')).toBe('/radar/images/x.svg')
    })
  })

  it('resolves to the root when the worker is served from the root', () => {
    asWorkerAt('/assets/layout.worker-abc.js', () => {
      expect(assetUrl('/images/x.svg')).toBe('/images/x.svg')
    })
  })

  // The base path may itself contain "/assets/" — split on the last one.
  it('splits on the last /assets/ segment', () => {
    asWorkerAt('/host/assets/radar/assets/layout.worker-abc.js', () => {
      expect(assetUrl('/images/x.svg')).toBe('/host/assets/radar/images/x.svg')
    })
  })

  it('prefers the injected config over the URL heuristic', () => {
    setAssetBase('/radar')
    asWorkerAt('/somewhere/assets/worker.js', () => {
      expect(assetUrl('/images/x.svg')).toBe('/radar/images/x.svg')
    })
  })
})
