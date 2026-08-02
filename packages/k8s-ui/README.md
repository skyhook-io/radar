# @skyhook-io/k8s-ui

Shared, source-distributed Kubernetes UI components used by Radar.

## YAML editor bundling

`YamlEditor` and `YamlDiffEditor` bundle Monaco, its editor worker, and the YAML language worker into the consuming application. They make no runtime CDN or internet requests, so they work in air-gapped environments.

Consumers must use a bundler that supports module workers created with `new Worker(new URL(..., import.meta.url), { type: 'module' })`. The source package is compatible with Vite and webpack 5. Monaco and `monaco-yaml` are intentionally pinned as a compatible pair because their worker-factory APIs must move together. The package declares that exact Monaco version as a peer so the host and YAML runtime share one Monaco instance.
