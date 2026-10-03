import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { CNPGObjectStoreRenderer } from './CNPGObjectStoreRenderer'

/**
 * A recovery window is keyed by the archive's server name. It defaults to the
 * cluster's name, but the plugin's `serverName` parameter overrides it — the
 * ObjectStore refuses the field outright and points at the Cluster — so the key
 * and the cluster name are not interchangeable. A link is an assertion that
 * something is on the other end of it.
 */
const store = {
  apiVersion: 'barmancloud.cnpg.io/v1',
  kind: 'ObjectStore',
  metadata: { name: 'pg-store', namespace: 'pg' },
  spec: { configuration: { destinationPath: 's3://backups' } },
  status: {
    serverRecoveryWindow: {
      'pg-main': { firstRecoverabilityPoint: '2026-08-01T00:00:00Z', lastSuccessfulBackupTime: '2026-08-10T00:00:00Z' },
    },
  },
}

const nav = () => {}

describe('ObjectStore recovery-window server key', () => {
  it('links the key to the cluster archiving under it', () => {
    const html = renderToString(
      <CNPGObjectStoreRenderer data={store} clusterForServer={new Map([['pg-main', 'pg-main']])} onNavigate={nav} />,
    )
    expect(html).toContain('>pg-main</button>')
  })

  // A key belonging to no cluster we found — an in-tree cluster, or one whose
  // archive nothing here accounts for.
  it('renders the key as text when nothing archives under it', () => {
    const html = renderToString(
      <CNPGObjectStoreRenderer data={store} clusterForServer={new Map([['other-server', 'other-cluster']])} onNavigate={nav} />,
    )
    expect(html).toContain('pg-main')
    expect(html).not.toContain('>pg-main</button>')
  })

  // Absent means unresolved — still loading, lookup failed, or the host cannot
  // ask. None of those establish that the cluster is there.
  it('does not link when nothing resolved the key', () => {
    const html = renderToString(<CNPGObjectStoreRenderer data={store} onNavigate={nav} />)
    expect(html).toContain('pg-main')
    expect(html).not.toContain('>pg-main</button>')
  })

  // The whole reason the mapping exists: the key is the archive's name, and a
  // cluster that renamed its archive must still be reachable from it.
  it('links a renamed archive to the cluster behind it, showing the archive name', () => {
    const html = renderToString(
      <CNPGObjectStoreRenderer
        data={store}
        clusterForServer={new Map([['pg-main', 'pg-primary']])}
        onNavigate={nav}
      />,
    )
    expect(html).toContain('>pg-main</button>')
    expect(html).not.toContain('>pg-primary</button>')
  })
})

// The recovery window is read before a restore, and it is derived from the
// ObjectStore alone — which cannot see that WAL archiving stopped on the cluster
// feeding it. Left to itself the row keeps a green "Recoverable" and a
// four-day-old successful backup while the recovery point has frozen, and the
// warning lives on a different screen entirely.
describe('a window that has stopped advancing', () => {
  const store = {
    metadata: { name: 'pg-store', namespace: 'pg' },
    status: {
      serverRecoveryWindow: {
        'pg-doomed': {
          firstRecoverabilityPoint: '2026-07-29T02:00:00Z',
          lastSuccessfulBackupTime: '2026-08-12T02:00:00Z',
        },
      },
    },
  }

  it('does not call a frozen window recoverable', () => {
    const html = renderToString(
      <CNPGObjectStoreRenderer data={store} archivingFailing={new Set(['pg-doomed'])} />,
    )
    expect(html).toContain('Not advancing')
    expect(html).not.toContain('>Recoverable<')
    expect(html).toContain('WAL archiving has stopped')
  })

  it('keeps the plain answer when archiving is healthy', () => {
    const html = renderToString(<CNPGObjectStoreRenderer data={store} />)
    expect(html).toContain('Recoverable')
    expect(html).not.toContain('WAL archiving has stopped')
  })
})

describe('ObjectStore failing backups', () => {
  const failing = {
    ...store,
    status: { serverRecoveryWindow: { 'pg-main': { firstRecoverabilityPoint: '2026-08-01T00:00:00Z', lastSuccessfulBackupTime: '2026-08-10T00:00:00Z', lastFailedBackupTime: '2026-08-11T00:00:00Z' } } },
  }
  it('does not say recovery still reaches the newest WAL when archiving has stopped too', () => {
    const html = renderToString(<CNPGObjectStoreRenderer data={failing} archivingFailing={new Set(['pg-main'])} onNavigate={nav} />)
    expect(html).toContain('WAL archiving has stopped for pg-main')
    expect(html).not.toContain('recovery still reaches the newest archived WAL')
    expect(html).toContain('WAL archiving has stopped on the cluster behind this server')
  })
  it('keeps recovery reaching archived WAL when only base backups fail', () => {
    const html = renderToString(<CNPGObjectStoreRenderer data={failing} archivingFailing={new Set()} onNavigate={nav} />)
    expect(html).toContain('recovery still reaches the newest archived WAL')
  })
})
