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
  return typeof asset === 'string' ? asset : asset.src
}
