import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { stringify } from 'yaml'
import { CrossplaneApplyNotice, CrossplaneDeleteNotice, CrossplaneYamlApplyNotice } from './CrossplaneOperationNotice'

const mr = {
  apiVersion: 's3.example.test/v1', kind: 'Bucket',
  metadata: { name: 'archive', annotations: { 'crossplane.io/paused': 'true' }, finalizers: ['finalizer.managedresource.crossplane.io'] },
  spec: { providerConfigRef: { name: 'default' } },
}
const xr = { ...mr, kind: 'Composite', spec: { resourceRefs: [] }, metadata: { ...mr.metadata, finalizers: ['composite.apiextensions.crossplane.io'] } }
const claim = { ...mr, kind: 'Claim', spec: { resourceRef: { name: 'xr' }, compositionRef: { name: 'composition' } }, metadata: { ...mr.metadata, finalizers: ['finalizer.apiextensions.crossplane.io'] } }
const html = (element: React.ReactNode) => renderToStaticMarkup(element)

describe('Crossplane operation context', () => {
  it.each([mr, xr, claim])('identifies its own paused deletion cleanup for $kind', data => {
    const result = html(<CrossplaneDeleteNotice resources={[data]} />)
    expect(result).toContain('This resource holds a Crossplane finalizer')
    expect(result).toContain('Kubernetes can accept the deletion request')
    expect(result).toContain('defer finalizer cleanup')
    expect(result).not.toContain('stuck in Terminating')
    expect(result).toContain('requests resumption')
    expect(result).not.toContain('will delete external')
  })
  it('distinguishes an already requested deletion', () => {
    const result = html(<CrossplaneDeleteNotice resources={[{ ...mr, metadata: { ...mr.metadata, deletionTimestamp: '2026-10-26T00:00:00Z' } }]} />)
    expect(result).toContain('Deletion requested while reconciliation is paused')
    expect(result).toContain('Deletion has already been requested')
    expect(result).not.toContain('Kubernetes can accept')
  })
  it('avoids claiming pause alone blocks deletion without a known finalizer', () => {
    const result = html(<CrossplaneDeleteNotice resources={[{ ...mr, metadata: { ...mr.metadata, finalizers: ['other.example.test/cleanup'] } }]} />)
    expect(result).toContain('pause annotation alone does not block Kubernetes deletion')
    expect(result).not.toContain('cannot complete its finalizer cleanup')
  })
  it('explains the existing explicit force choice without promising a cascade', () => {
    const result = html(<CrossplaneDeleteNotice resources={[mr]} force />)
    expect(result).toContain('Force delete strips finalizers')
    expect(result).toContain('can leave external or related resources behind')
    expect(result).toContain('does not resume reconciliation')
    expect(result).toContain('This resource has finalizers')
    expect(result).not.toContain('These resources have finalizers')
  })
  it('names only the paused finalizer holders in a mixed blocker warning', () => {
    const bare = { ...mr, metadata: { ...mr.metadata, name: 'bare', finalizers: [] } }
    const active = { ...mr, metadata: { ...mr.metadata, name: 'active', annotations: {} } }
    const result = html(<CrossplaneDeleteNotice resources={[mr, bare, active]} />)
    expect(result).toContain('Of 3 selected resources, 1 has pause requested and holds')
    expect(result).toContain('Bucket archive')
    expect(result).not.toContain('Bucket bare')
    expect(result).not.toContain('Bucket active')
    const mixedForce = html(<CrossplaneDeleteNotice resources={[mr, bare, active]} force />)
    expect(mixedForce).toContain('Of 3 selected resources, 1 has pause requested and finalizers')
    const forced = html(<CrossplaneDeleteNotice resources={[bare]} force />)
    expect(forced).toContain('Bucket bare')
    expect(forced).not.toContain('0 of 1')
    expect(forced).not.toContain('Force delete bypasses')
    expect(forced).toContain('pause annotation alone does not block')
  })
  it('counts a mixed selection and caps names', () => {
    const resources = Array.from({ length: 7 }, (_, i) => ({ ...mr, metadata: { ...mr.metadata, name: `paused-${i}` } }))
    const active = { ...mr, metadata: { ...mr.metadata, name: 'active', annotations: {} } }
    const result = html(<CrossplaneDeleteNotice resources={[...resources, active]} />)
    expect(result).toContain('Of 8 selected resources, 7 have pause requested and hold')
    expect(result).toContain('and 2 more')
    expect(result).not.toContain('paused-6')
    expect(result).not.toContain('Bucket active')
  })
  it.each([undefined, { metadata: mr.metadata }, { ...mr, metadata: { annotations: { 'crossplane.io/paused': 'false' } } }, { ...mr, metadata: { annotations: { 'crossplane.io/paused': true } } }])('omits context for unrelated or unannotated input %#', data => {
    expect(html(<CrossplaneDeleteNotice resources={[data]} />)).toBe('')
    expect(html(<CrossplaneApplyNotice resources={[data]} />)).toBe('')
  })
  it('retains own-controller context while independent failures exist', () => {
    const data = { ...mr, status: { conditions: [{ type: 'Ready', status: 'False', reason: 'Unavailable' }] } }
    expect(html(<CrossplaneApplyNotice resources={[data]} />)).toContain('Other resources&#x27; controllers may continue independently')
    expect(data.status.conditions[0].reason).toBe('Unavailable')
  })
  it('uses conditional wording when an apply draft omits a pause owned by another manager', () => {
    const after = { ...mr, metadata: { ...mr.metadata, annotations: {} } }
    const result = html(<CrossplaneYamlApplyNotice yaml={stringify(after)} baselineYaml={stringify(mr)} />)
    expect(result).toContain('Kubernetes can accept edits')
    expect(result).toContain('requests resumption')
    expect(result).toContain('Omitting the annotation from server-side apply YAML does not guarantee its removal')
    expect(result).not.toContain('will resume')
  })
  it('detects pause added by the submitted or predicted manifest', () => {
    expect(html(<CrossplaneYamlApplyNotice yaml={stringify(mr)} />)).toContain('Pause affects reconciliation')
  })
  it('names unknown live pause state only for apply without a baseline', () => {
    const unpausedDraft = { ...mr, metadata: { ...mr.metadata, annotations: {} } }
    const yaml = stringify(unpausedDraft)
    const result = html(<CrossplaneYamlApplyNotice yaml={yaml} baselineUnavailable />)
    expect(result).toContain('Existing pause state is unknown')
    expect(result).toContain('Omitting the annotation from server-side apply YAML does not guarantee its removal')
    expect(result).not.toContain('Pause affects reconciliation')
    expect(html(<CrossplaneYamlApplyNotice yaml={yaml} />)).toBe('')
    expect(html(<CrossplaneYamlApplyNotice yaml="kind: ConfigMap" baselineUnavailable />)).toBe('')
  })
  it('retains explicit submitted pause context alongside unknown live state', () => {
    const result = html(<CrossplaneYamlApplyNotice yaml={stringify(mr)} baselineUnavailable />)
    expect(result).toContain('Pause affects reconciliation')
    expect(result).toContain('Live pause state is unverified')
    expect(result).toContain('requests resumption')
  })
  it('recognizes acknowledged ref-less objects without broadening annotation-only detection', () => {
    const raw = { ...mr, kind: 'Claim', spec: {}, status: { conditions: [{ type: 'Synced', status: 'False', reason: 'ReconcilePaused' }] } }
    expect(html(<CrossplaneDeleteNotice resources={[raw]} />)).toContain('This resource holds a Crossplane finalizer')
    const { status: _status, ...clean } = raw
    expect(html(<CrossplaneYamlApplyNotice yaml={stringify(clean)} baselineUnavailable />)).toBe('')
    expect(html(<CrossplaneApplyNotice resources={[{ ...raw, metadata: { ...raw.metadata, annotations: {} } }]} />)).toBe('')
  })
  it('binds raw acknowledgement to identity across versions without borrowing it for another API group', () => {
    const raw = { ...mr, kind: 'Claim', spec: {}, status: { conditions: [{ type: 'Synced', status: 'False', reason: 'ReconcilePaused' }] } }
    const check = (apiVersion: string | number) => {
      const yaml = stringify({ ...raw, apiVersion, status: undefined })
      return html(<CrossplaneYamlApplyNotice yaml={yaml} baselineYaml={yaml} resourceData={raw} previewDocuments={[{ index: 0, status: 'accepted', baselineYaml: yaml, predictedYaml: yaml }]} />)
    }
    expect(check('another.example.test/v1')).toBe('')
    expect(check('s3.example.test/v2')).toContain('Server preview keeps the pause request')
    expect(() => check(42)).not.toThrow()
  })
  it('does not borrow old raw acknowledgement through an old baseline when the effective target changes group', () => {
    const raw = { ...mr, kind: 'Claim', spec: {}, status: { conditions: [{ type: 'Synced', status: 'False', reason: 'ReconcilePaused' }] } }
    const baselineYaml = stringify({ ...raw, status: undefined })
    const predictedYaml = stringify({ ...raw, apiVersion: 'another.example.test/v1', status: undefined })
    const result = html(<CrossplaneYamlApplyNotice yaml={predictedYaml} baselineYaml={baselineYaml} resourceData={raw} previewDocuments={[{ index: 0, status: 'accepted', action: 'update', baselineYaml, predictedYaml }]} />)
    expect(result).toBe('')
  })
  it.each(['direct', 'unavailable'] as const)('does not borrow a paused shape from another group through the original baseline in %s review', mode => {
    const baselineYaml = stringify(mr)
    const submittedYaml = stringify({ apiVersion: 'another.example.test/v1', kind: mr.kind, metadata: { name: mr.metadata.name }, spec: {} })
    const result = html(<CrossplaneYamlApplyNotice yaml={submittedYaml} baselineYaml={baselineYaml} resourceData={mr} previewDocuments={mode === 'unavailable' ? [{ index: 0, status: 'unavailable', action: 'unknown', submittedYaml }] : undefined} />)
    expect(result).toBe('')
  })
  it('retains same-target pause clearing across served versions when the submitted shape changes', () => {
    const baselineYaml = stringify(mr)
    const predictedYaml = stringify({ ...mr, apiVersion: 's3.example.test/v2', metadata: { ...mr.metadata, annotations: { 'crossplane.io/paused': 'false' } }, spec: {} })
    const result = html(<CrossplaneYamlApplyNotice yaml={predictedYaml} baselineYaml={baselineYaml} previewDocuments={[{ index: 0, status: 'accepted', action: 'update', baselineYaml, predictedYaml }]} />)
    expect(result).toContain('Server preview clears the pause request on Bucket archive')
    expect(result).toContain('requests resumption')
  })
  it('does not use one raw target to classify unrelated documents in a batch', () => {
    const raw = { ...mr, kind: 'Claim', spec: {}, status: { conditions: [{ type: 'Synced', status: 'False', reason: 'ReconcilePaused' }] } }
    const clean = { ...raw, status: undefined }
    const yaml = stringify(clean)
    const result = html(<CrossplaneYamlApplyNotice yaml={yaml + '\n---\n' + yaml} resourceData={raw} previewDocuments={[{ index: 0, status: 'accepted', baselineYaml: yaml, predictedYaml: yaml }, { index: 1, status: 'accepted', baselineYaml: yaml, predictedYaml: yaml }]} />)
    expect(result).toBe('')
  })
  it('handles invalid or empty YAML without disturbing the editor validation', () => {
    expect(html(<CrossplaneYamlApplyNotice yaml={'spec: [\n'} />)).toBe('')
    expect(html(<CrossplaneYamlApplyNotice yaml="" />)).toBe('')
  })
})
