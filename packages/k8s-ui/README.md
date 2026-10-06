# @skyhook-io/k8s-ui

Shared, source-distributed Kubernetes UI components used by Radar.

## Local terminal metadata

`LocalTerminalTab` accepts optional `onSessionInfo` and `toolbarExtra` props.
`onSessionInfo` reports the server's `session` frame (`context` and
`kubeconfigIsolated`), and receives `null` when a connection attempt starts.
This describes the supplied kubeconfig, not a shell's live command target.
`toolbarExtra` lets the host render that information in the terminal toolbar.
Hosts can update a dock tab's label and optional full-name tooltip with
`useDock().setTabTitle(id, title, titleTooltip)`.
The optional `DockTab.localTerminalContext` field lets a host retain the full
context selected when a local terminal was requested. An empty string records
no active context; an omitted field records no intent. The shared terminal and
open hook do not interpret it; Radar's host wrapper checks it on open/reconnect.
`canConnect` is a predicate evaluated during rendering to disable Reconnect/Retry,
and before an attempt to preserve the existing terminal when refused.
`onConnectionError` lets a host refresh connection state after a failed WebSocket
handshake.

`initialCommand` is sent once per mounted terminal. Reconnect does not repeat a
command that was already sent; if the connection closes before delivery, the
command remains pending for the next connection.

## YAML editor bundling

`YamlEditor` and `YamlDiffEditor` bundle Monaco, its editor worker, and the YAML language worker into the consuming application. They make no runtime CDN or internet requests, so they work in air-gapped environments.

Consumers must use a bundler that supports module workers created with `new Worker(new URL(..., import.meta.url), { type: 'module' })`. The source package is compatible with Vite and webpack 5. Monaco and `monaco-yaml` are intentionally pinned as a compatible pair because their worker-factory APIs must move together. The package declares that exact Monaco version as a peer so the host and YAML runtime share one Monaco instance.

`CreateResourceDialog` accepts optional `onBack(yaml)` and `backLabel` props for
parent flows. Back returns the current editor draft without invoking `onClose`;
it is disabled during writes, previews and file imports. The review screen
continues to return to the editor first. Hosts own draft retention and any
confirmation before replacing edited YAML.
