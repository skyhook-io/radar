# Isolated context tabs

Radar's standalone local binary can open another kubeconfig context in a new
browser tab without changing the cluster used by the current tab.

Open the context switcher and choose the new-tab icon on any non-current
context row. Radar starts a sibling process on a loopback port with that
context's kubeconfig source. The sibling has its own Kubernetes clients,
informers, caches, SSE stream, and integration state, so switching the parent
tab no longer retargets the new tab.

This feature is deliberately limited to unauthenticated standalone Radar on a
loopback listener. It is disabled for shared listeners, OIDC/proxy-auth
deployments, Cloud/in-cluster mode, and in-cluster Radar, where a separate
process cannot safely inherit the deployment's authentication or tunnel
session. The existing **Switch context** action remains available for changing
the current tab.

Child processes are tracked by the parent and terminated when the parent Radar
process shuts down.
