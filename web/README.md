# @skyhook-io/radar-app

Radar's full web UI as a reusable React component. Used by Radar's own binary and by external host apps (e.g. Radar Hub) that want to embed the Radar UI inside their own frontend.

This package is source-only — it ships TypeScript + TSX files under `src/`. Consumers need a bundler that transpiles TSX (Vite, Next.js, esbuild, etc.).

## Install

```sh
npm install @skyhook-io/radar-app
```

Peer deps: `react >=19`, `react-dom >=19`, `react-router-dom >=7`, `@tanstack/react-query >=5`, `@skyhook-io/k8s-ui >=1.5.0`, plus `clsx`, `tailwind-merge`, `lucide-react`, `@xyflow/react`, `elkjs`.

## Use

```tsx
import { RadarApp, setApiBase, setAuthHeadersProvider } from '@skyhook-io/radar-app';
import '@skyhook-io/radar-app/style.css';

setAuthHeadersProvider(() => ({ Authorization: `Bearer ${myToken()}` }));

export function ClusterPage({ clusterId }: { clusterId: string }) {
  return (
    <RadarApp
      apiBase={`/c/${clusterId}/api`}
      basename={`/c/${clusterId}`}
      navSlots={{
        embedded: true,
        brandSlot: <MyBrand />,
        contextSlot: <MyClusterSwitcher />,
        rightExtras: <MyUserMenu />,
      }}
    />
  );
}
```

See `RadarAppProps` + `NavCustomization` in the type declarations for the full surface.

## Tailwind

Radar uses Tailwind v4 classes throughout. Your app's Tailwind config must scan Radar's source:

```js
// tailwind.config.js or equivalent
content: [
  './src/**/*.{ts,tsx}',
  './node_modules/@skyhook-io/radar-app/src/**/*.{ts,tsx}',
  './node_modules/@skyhook-io/k8s-ui/src/**/*.{ts,tsx}',
],
```

## Runtime config

Host apps can override Radar's runtime behavior without passing props:

- `setApiBase(url)` — base URL for REST/SSE/WS requests. Default `/api`.
- `setBasename(value)` — React Router basename. Default `''`.
- `setAuthHeadersProvider(fn)` — returns headers (e.g. `Authorization`) to attach to every request.
- `setCredentialsMode(mode)` — fetch credentials mode (`same-origin` | `include` | `omit`).

Call these before mounting `<RadarApp>`.

## Backwards compatibility

The `RadarApp` props (`apiBase`, `basename`, `router`, `navSlots`, `queryClient`) and the runtime-config setters are the stable surface. Adding to them is fine; removing or renaming is a breaking change.
