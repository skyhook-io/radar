// k8s-ui ships TS source, so each consumer's bundler types asset imports
// differently: Vite resolves `import x from './x.png'` to a URL string, while
// webpack/Next resolves it to a StaticImageData object. Reading `.src` off the
// object (vs Vite's bare string) yields the bundler's own emitted URL, which is
// the one that's correct in both client and SSR output — unlike
// `new URL(asset, import.meta.url)`, which can produce a `file://` URL under SSR.
// The structural `{ src: string }` type matches Next's StaticImageData without
// depending on the Next-only type, so this also type-checks under Vite.
export type AssetImport = string | { src: string }

export function assetUrl(asset: AssetImport): string {
  const url = typeof asset === 'string' ? asset : asset.src
  const assetBase = getRuntimeAssetBase()
  if (!assetBase || url.startsWith('//') || url === assetBase || url.startsWith(`${assetBase}/`)) {
    if (url.startsWith('./assets/')) return `/assets/${url.slice('./assets/'.length)}`
    if (url.startsWith('assets/')) return `/${url}`
    return url
  }
  if (url.startsWith('./')) return `${assetBase}/${url.slice(2)}`
  if (!url.startsWith('/') && url.startsWith('assets/')) return `${assetBase}/${url}`
  if (!url.startsWith('/')) return url
  return `${assetBase}${url}`
}

function getRuntimeAssetBase(): string {
  if (typeof globalThis === 'undefined') return ''
  const runtime = (globalThis as typeof globalThis & {
    __RADAR_RUNTIME_CONFIG__?: { assetBase?: string }
  }).__RADAR_RUNTIME_CONFIG__
  const configured = runtime?.assetBase?.replace(/\/+$/, '')
  if (configured) return configured

  const pathname = globalThis.location?.pathname ?? ''
  const assetsIndex = pathname.indexOf('/assets/')
  if (assetsIndex > 0) return pathname.slice(0, assetsIndex)
  return ''
}
