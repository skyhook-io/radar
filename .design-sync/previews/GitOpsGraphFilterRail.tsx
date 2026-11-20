import { GitOpsGraphFilterRail } from '@skyhook-io/k8s-ui'

// The filter rail beside the GitOps detail page's topology graph, for an
// Argo CD Application with a degraded Deployment.
const facets = {
  kinds: [{ name: 'Deployment', count: 3 }, { name: 'Service', count: 3 }, { name: 'ConfigMap', count: 4 }, { name: 'Ingress', count: 1 }],
  sync: [{ name: 'Synced', count: 9 }, { name: 'OutOfSync', count: 2 }],
  health: [{ name: 'Healthy', count: 9 }, { name: 'Degraded', count: 1 }, { name: 'Progressing', count: 1 }],
  namespaces: [{ name: 'payments', count: 10 }, { name: '(cluster)', count: 1 }],
  roles: [{ name: 'root', count: 1 }, { name: 'declared', count: 8 }, { name: 'generated', count: 3 }],
}
const noop = () => {}
// Below the lg breakpoint the rail caps itself at h-48 and scrolls; the card shows
// the desktop layout, where it fills the column beside the graph.
const desktopRail = '.desktop-rail aside { height: auto !important; max-height: none !important; }'

export function Default() {
  return (
    <div className="desktop-rail" style={{ width: 260 }}>
      <style>{desktopRail}</style>
      <GitOpsGraphFilterRail
        facets={facets}
        preset="compact"
        onPresetChange={noop}
        search=""
        onSearchChange={noop}
        kinds={new Set()}
        onToggleKind={noop}
        sync={new Set(['OutOfSync'])}
        onToggleSync={noop}
        health={new Set(['Degraded'])}
        onToggleHealth={noop}
        namespaces={new Set()}
        onToggleNamespace={noop}
        roles={new Set()}
        onToggleRole={noop}
      />
    </div>
  )
}
