import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { VeleroRestoreRenderer } from './VeleroRestoreRenderer'

const restore = (status: Record<string, unknown>) => ({
  metadata: { name: 'live-restore', namespace: 'velero' },
  spec: { backupName: 'live-completed' },
  status,
})

const scopedRestore = (spec: Record<string, unknown>) => ({
  metadata: { name: 'live-restore', namespace: 'velero' },
  spec: { backupName: 'live-completed', ...spec },
  status: { phase: 'Completed' },
})

/**
 * The restore side carries the same claims as the backup side and its own
 * wording for them, so pinning one does not pin the other. Both banners below
 * were wrong in the same way at the same time.
 */
describe('what a restore claims about itself', () => {
  // A count taken mid-run is not a final count. Velero raises warnings as it
  // goes, so a restore still working can already have several.
  it('does not say a restore in flight completed', () => {
    const html = renderToString(
      <VeleroRestoreRenderer data={restore({ phase: 'InProgress', warnings: 2 })} />,
    )
    expect(html).not.toMatch(/completed, with/)
    expect(html).toContain('not a final count')
  })

  it('still says completed when it did', () => {
    const html = renderToString(
      <VeleroRestoreRenderer data={restore({ phase: 'Completed', warnings: 2 })} />,
    )
    expect(html).toContain('completed, with 2 warning(s)')
    expect(html).not.toContain('not a final count')
  })

  // Velero's own words beat ours whenever it supplies them.
  it('leads with the reason Velero gave for refusing to start', () => {
    const html = renderToString(
      <VeleroRestoreRenderer
        data={restore({ phase: 'FailedValidation', failureReason: 'backup not found' })}
      />,
    )
    expect(html).toContain('backup not found')
    expect(html).not.toContain('Velero rejected this restore before it started')
  })

  it('says nothing was restored when Velero gave no reason', () => {
    const html = renderToString(
      <VeleroRestoreRenderer
        data={restore({ phase: 'FailedValidation', validationErrors: ['Backup live-completed not found'] })}
      />,
    )
    expect(html).toContain('nothing was restored')
    expect(html).toContain('Backup live-completed not found')
  })
})

/**
 * Scope is where an operator reads what a restore will touch and where it lands.
 * namespaceMapping remaps source namespaces to different targets on restore, so
 * omitting it hides where the data actually goes; a labelSelector narrows the
 * restore to a subset of objects.
 */
// react-dom/server inserts <!-- --> markers between adjacent text nodes, which
// splits a badge like `app=nginx` in the raw markup; strip them so assertions
// read the visible text.
const renderScope = (data: unknown) =>
  renderToString(<VeleroRestoreRenderer data={data} />).replace(/<!-- -->/g, '')

describe('what a restore says it will touch', () => {
  it('shows namespaceMapping as source to target', () => {
    const html = renderScope(
      scopedRestore({ namespaceMapping: { prod: 'prod-clone', staging: 'staging-clone' } }),
    )
    expect(html).toContain('Scope')
    expect(html).toContain('Namespace Mapping')
    expect(html).toContain('prod')
    expect(html).toContain('prod-clone')
    expect(html).toContain('staging-clone')
  })

  it('shows the label selector that narrows the restore', () => {
    const html = renderScope(
      scopedRestore({
        labelSelector: {
          matchLabels: { app: 'nginx' },
          matchExpressions: [{ key: 'tier', operator: 'In', values: ['frontend'] }],
        },
      }),
    )
    expect(html).toContain('Scope')
    expect(html).toContain('app=nginx')
    expect(html).toContain('tier In frontend')
  })

  it('renders a Scope section for a restore filtered only by label selector', () => {
    const html = renderScope(scopedRestore({ labelSelector: { matchLabels: { app: 'nginx' } } }))
    expect(html).toContain('Scope')
    expect(html).toContain('app=nginx')
  })

  it('shows orLabelSelectors joined by OR', () => {
    const html = renderScope(
      scopedRestore({
        orLabelSelectors: [{ matchLabels: { app: 'nginx' } }, { matchLabels: { app: 'redis' } }],
      }),
    )
    expect(html).toContain('Scope')
    expect(html).toContain('app=nginx')
    expect(html).toContain('app=redis')
  })

  it('shows included and excluded namespaces alongside mapping', () => {
    const html = renderScope(
      scopedRestore({
        includedNamespaces: ['prod'],
        excludedNamespaces: ['kube-system'],
        namespaceMapping: { prod: 'prod-clone' },
      }),
    )
    expect(html).toContain('Included Namespaces')
    expect(html).toContain('Excluded Namespaces')
    expect(html).toContain('kube-system')
    expect(html).toContain('prod-clone')
  })

  it('renders no empty Scope section for an unscoped restore', () => {
    const html = renderScope(scopedRestore({}))
    expect(html).not.toContain('Scope')
  })

  // An empty selector matches everything, so it is not scope. Presenting it as
  // scope would tell an operator a restore is filtered when it is not.
  it('treats an empty label selector as unscoped', () => {
    const html = renderScope(scopedRestore({ labelSelector: {}, orLabelSelectors: [{}] }))
    expect(html).not.toContain('Scope')
  })
})
