import { Disclosure } from '@skyhook-io/k8s-ui'

const box = { width: 440 } as const

export function TechnicalDetailsClosed() {
  return (
    <div style={box}>
      <Disclosure
        className="text-xs text-theme-text-tertiary"
        summaryClassName="hover:text-theme-text-secondary"
        summary="Technical details"
      >
        <pre className="mt-2 whitespace-pre-wrap font-mono text-[11px]">
          exec /bin/cat /var/log/nginx/access.log: permission denied (container runs as uid 101)
        </pre>
      </Disclosure>
    </div>
  )
}

export function TroubleshootingOpen() {
  return (
    <div style={box}>
      <Disclosure
        defaultOpen
        className="border-t border-theme-border pt-4"
        summaryClassName="font-medium text-theme-text-primary text-sm"
        chevronClassName="h-4 w-4"
        summary="Troubleshooting identity matching"
      >
        <ul style={{ marginTop: 8, paddingLeft: 20, listStyle: 'disc', display: 'grid', gap: 4 }} className="text-xs text-theme-text-secondary">
          <li>Metrics are matched on the <code>namespace</code> and <code>pod</code> labels.</li>
          <li>Relabeling that drops <code>pod</code> leaves the chart empty, not zero.</li>
          <li>Check the scrape job with <code>up{'{'}job="kubelet"{'}'}</code>.</li>
        </ul>
      </Disclosure>
    </div>
  )
}

export function MemberIndicesOpen() {
  return (
    <div style={box} className="rounded-lg border border-theme-border bg-theme-surface">
      <Disclosure defaultOpen summary="Member indices" className="px-4 py-3" summaryClassName="text-xs text-theme-text-tertiary">
        <div className="flex flex-wrap gap-x-4 gap-y-1 pt-2 text-xs text-theme-text-secondary">
          <span>Succeeded: 0-5, 7</span>
          <span>Failed: 6</span>
          <span>Active: 8-11</span>
        </div>
      </Disclosure>
    </div>
  )
}
