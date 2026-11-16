import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { VeleroBackupRenderer } from './VeleroBackupRenderer'
import { OperationalIssuesShownContext } from '../../ui/drawer-components'

const backup = (location: string) => ({
  metadata: { name: 'nightly', namespace: 'velero' },
  spec: { storageLocation: location, ttl: '720h0m0s' },
  status: { phase: 'Completed', startTimestamp: '2026-08-14T01:00:00Z' },
})

/**
 * A backup cannot see the bucket behind it. Its own status says Completed and
 * keeps saying it after the storage location goes Unavailable, which is exactly
 * when someone opens this page to ask whether they can restore to this point.
 */
describe('a backup whose storage location is unreachable', () => {
  it('says so on the backup, not one screen away', () => {
    const html = renderToString(
      <VeleroBackupRenderer data={backup('dr-replica')} storageLocationPhase="Unavailable" />,
    )
    expect(html).toContain('nothing here to restore from')
    expect(html).toContain('Unavailable')
  })

  // On a healthy location this would be a warning about nothing, on every backup
  // in the cluster.
  it('stays quiet when the location is fine', () => {
    const html = renderToString(
      <VeleroBackupRenderer data={backup('default')} storageLocationPhase="Available" />,
    )
    expect(html).not.toContain('nothing here to restore from')
  })

  // The distinction that matters: a location we have not read is not a healthy
  // one, and must not render as one.
  it('claims nothing while the lookup is unresolved', () => {
    const html = renderToString(<VeleroBackupRenderer data={backup('default')} />)
    expect(html).not.toContain('nothing here to restore from')
    expect(html).not.toContain('Unavailable')
  })
})

/**
 * FailedValidation is the one outcome where Velero usually explains itself, in
 * status.failureReason. Radar's own sentence is the fallback for when it does
 * not — a run rejected before it started leaves no errors, no warnings and no
 * item counts, so without either the page is an empty red banner.
 */
describe('a run Velero refused to start', () => {
  const rejected = (failureReason?: string) => ({
    metadata: { name: 'live-rejected', namespace: 'velero' },
    spec: { storageLocation: 'dr-replica' },
    status: {
      phase: 'FailedValidation',
      ...(failureReason ? { failureReason } : {}),
      validationErrors: ['Backup storage location "dr-replica" is unavailable'],
    },
  })

  // Velero's own words beat ours whenever it supplies them.
  it('leads with the reason Velero gave', () => {
    const html = renderToString(
      <VeleroBackupRenderer data={rejected('backup storage location is in read-only mode')} />,
    )
    expect(html).toContain('backup storage location is in read-only mode')
    expect(html).not.toContain('Velero rejected this backup before it started')
  })

  it('says nothing was backed up when Velero gave no reason', () => {
    const html = renderToString(<VeleroBackupRenderer data={rejected()} />)
    expect(html).toContain('nothing was backed up')
    // The validation errors are the actionable half either way.
    expect(html).toContain('dr-replica&quot; is unavailable')
  })
})

/**
 * Velero writes the resolved location into spec.storageLocation when it first
 * processes a backup. An unset field means it has not done so yet, and the
 * default it will pick can still change, so the page names no location.
 */
describe('a backup Velero has not assigned a location yet', () => {
  const unset = {
    metadata: { name: 'nightly', namespace: 'velero' },
    spec: { ttl: '720h0m0s' },
    status: { phase: 'New' },
  }

  it('says the location is not assigned and links nothing', () => {
    const html = renderToString(<VeleroBackupRenderer data={unset} onNavigate={() => {}} />)
    const i = html.indexOf('Storage Location')
    const cell = html.slice(i, html.indexOf('</div>', i))
    expect(cell).toContain('Not assigned yet')
    expect(cell).not.toContain('<button')
    expect(cell).not.toContain('>default<')
  })
})

/**
 * Three states, not two. The lookup can be unanswered, answered-and-found, or
 * answered-and-the-location-is-gone — and the last one has no phase to report,
 * so without saying it the backup renders exactly as it would on a healthy
 * bucket.
 */
describe('what the page claims before the location lookup answers', () => {
  const named = {
    metadata: { name: 'nightly', namespace: 'velero' },
    spec: { storageLocation: 'dr-replica' },
    status: { phase: 'Completed' },
  }

  // Scoped to the Storage Location row: "default" is also the rendered value of
  // two unset fields in Options, so a whole-document match proves nothing.
  const storageCell = (html: string) => {
    const i = html.indexOf('Storage Location')
    if (i < 0) return ''
    // Up to the end of this property row only. A fixed-width window runs into
    // the next Section's header, which is itself a <button>, so every case
    // matched a link whether or not one was rendered.
    const end = html.indexOf('</div>', i)
    return html.slice(i, end < 0 ? undefined : end)
  }

  // A backup that names its own location is known regardless of the lookup.
  it('still shows a location the backup names itself', () => {
    const html = renderToString(<VeleroBackupRenderer data={named} onNavigate={() => {}} />)
    expect(html).toContain('dr-replica')
    expect(html).not.toContain('Not assigned yet')
  })

  it('says so when the location it names is gone', () => {
    const html = renderToString(
      <VeleroBackupRenderer data={named} storageLocationMissing onNavigate={() => {}} />,
    )
    expect(html).toContain('Not found')
    expect(html).toContain('no longer exists')
  })

  // A link to an object that is not there is worse than plain text. Scoped to
  // the cell: every collapsible Section header is itself a <button>, so the
  // whole document always matches.
  it('does not link a location that is gone', () => {
    const missing = storageCell(renderToString(
      <VeleroBackupRenderer data={named} storageLocationMissing onNavigate={() => {}} />,
    ))
    const present = storageCell(renderToString(
      <VeleroBackupRenderer data={named} onNavigate={() => {}} />,
    ))
    expect(present).toContain('<button')
    expect(missing).not.toContain('<button')
  })
})

/**
 * The drawer lists the issue a failed run raises above the renderer. Saying it
 * again in a box of its own showed the same failure twice, one under the other.
 */
describe('a failed run whose issue the drawer already shows', () => {
  const run = (phase: string, extra: Record<string, unknown> = {}) => ({
    metadata: { name: 'live-partial', namespace: 'velero' },
    spec: { storageLocation: 'default' },
    status: { phase, errors: 1, ...extra },
  })
  const withIssues = (data: any) =>
    renderToString(
      <OperationalIssuesShownContext.Provider value>
        <VeleroBackupRenderer data={data} />
      </OperationalIssuesShownContext.Provider>,
    )

  it('leaves the failure to the issue', () => {
    for (const phase of ['PartiallyFailed', 'Failed', 'FailedValidation']) {
      const html = withIssues(run(phase))
      expect(html, phase).not.toContain('Partially Failed')
      expect(html, phase).not.toContain('Backup Failed')
      expect(html, phase).not.toContain('Validation Failed')
    }
  })

  it('still describes it when no issue does', () => {
    expect(renderToString(<VeleroBackupRenderer data={run('PartiallyFailed')} />)).toContain('Do not assume this backup is complete')
  })

  // Warnings raise no issue, so this box is the only place they are said.
  it('keeps the warnings box', () => {
    expect(withIssues(run('Completed', { errors: 0, warnings: 2 }))).toContain('2 Warning(s)')
  })
})
