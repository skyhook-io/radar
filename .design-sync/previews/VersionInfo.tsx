import { VersionInfo } from '@skyhook-io/k8s-ui'
import type { AppRow, AppWorkload } from '@skyhook-io/k8s-ui'

const list = { display: 'flex', flexDirection: 'column', gap: 2, width: 340 } as const
const rowCell = {
  display: 'grid',
  gridTemplateColumns: '1fr auto',
  alignItems: 'center',
  gap: 12,
  padding: '8px 12px',
  border: '1px solid var(--border-default)',
  background: 'var(--bg-surface)',
}
const nameCell = { fontSize: 13, color: 'var(--text-primary)' }

function wl(kind: string, name: string, image: string, version: string): AppWorkload {
  return { kind, namespace: 'production', name, image, version, health: 'healthy', ready: 3, desired: 3, restarts: 0 }
}

const singleVersion: AppRow = {
  key: 'checkout',
  name: 'checkout',
  health: 'healthy',
  appVersion: 'v2.4.1',
  versions: ['v2.4.1'],
  workloads: [wl('Deployment', 'checkout', 'ghcr.io/skyhook/checkout:v2.4.1', 'v2.4.1')],
}

const longTag: AppRow = {
  key: 'ingester',
  name: 'ingester',
  health: 'healthy',
  versions: ['1.9.0-rc.4+build.20240517'],
  workloads: [wl('Deployment', 'ingester', 'registry.internal/ingester:1.9.0-rc.4+build.20240517', '1.9.0-rc.4')],
}

const multiNoSkew: AppRow = {
  key: 'platform',
  name: 'platform',
  health: 'healthy',
  versions: ['v1.2.0', 'v3.4.1', 'v0.9.7'],
  versionSkew: false,
  workloads: [
    wl('Deployment', 'api', 'ghcr.io/skyhook/api:v3.4.1', 'v3.4.1'),
    wl('Deployment', 'worker', 'ghcr.io/skyhook/worker:v1.2.0', 'v1.2.0'),
    wl('StatefulSet', 'cache', 'redis:v0.9.7', 'v0.9.7'),
  ],
}

const multiSkew: AppRow = {
  key: 'payments',
  name: 'payments',
  health: 'degraded',
  versions: ['v2.4.1', 'v2.3.0'],
  versionSkew: true,
  workloads: [
    wl('Deployment', 'payments-web', 'ghcr.io/skyhook/payments:v2.4.1', 'v2.4.1'),
    wl('Deployment', 'payments-web-canary', 'ghcr.io/skyhook/payments:v2.3.0', 'v2.3.0'),
  ],
}

const noVersion: AppRow = {
  key: 'legacy-job',
  name: 'legacy-job',
  health: 'unknown',
  versions: [],
  workloads: [wl('CronJob', 'legacy-job', 'busybox', '')],
}

function Cell({ label, app }: { label: string; app: AppRow }) {
  return (
    <div style={rowCell}>
      <span style={nameCell}>{label}</span>
      <VersionInfo app={app} variant="cell" />
    </div>
  )
}

export function TableCells() {
  return (
    <div style={list}>
      <Cell label="checkout" app={singleVersion} />
      <Cell label="ingester" app={longTag} />
      <Cell label="platform" app={multiNoSkew} />
      <Cell label="payments" app={multiSkew} />
      <Cell label="legacy-job" app={noVersion} />
    </div>
  )
}

export function FactVariant() {
  const strip = {
    display: 'flex',
    width: 'fit-content',
    gap: 24,
    padding: '12px 16px',
    border: '1px solid var(--border-default)',
    borderRadius: 8,
    background: 'var(--bg-surface)',
  }
  const item = { display: 'flex', flexDirection: 'column', gap: 4 } as const
  const cap = {
    fontSize: 10,
    fontWeight: 600,
    letterSpacing: '0.05em',
    textTransform: 'uppercase',
    color: 'var(--text-tertiary)',
  } as const
  return (
    <div style={strip}>
      <div style={item}>
        <span style={cap}>Version</span>
        <VersionInfo app={singleVersion} variant="fact" />
      </div>
      <div style={item}>
        <span style={cap}>Version</span>
        <VersionInfo app={multiNoSkew} variant="fact" />
      </div>
    </div>
  )
}
