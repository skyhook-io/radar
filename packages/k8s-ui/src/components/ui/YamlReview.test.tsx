import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { YamlReview } from './YamlReview'

const doc = { index: 0, kind: 'ConfigMap', name: 'app', namespace: 'shop', status: 'ok' as const, action: 'update' as const, reviewedResourceVersion: '42' }

function review(changedSinceReview?: boolean, applyError?: string) {
  return renderToString(
    <YamlReview
      submittedYaml="kind: ConfigMap"
      documents={[doc]}
      changedSinceReview={changedSinceReview}
      applyError={applyError}
      onBack={() => {}}
      onApply={() => {}}
    />,
  )
}

describe('YamlReview after a failed apply', () => {
  it('says the review now shows the latest version when the resource changed under it', () => {
    const html = review(true, 'resource changed after review; review the latest version before applying')
    expect(html).toContain('This resource changed after your review')
    expect(html).toContain('apply again')
    // Said once: the raw API message would repeat the notice.
    expect(html).not.toContain('review the latest version before applying')
  })

  it('keeps an unrelated apply error beside the notice', () => {
    const html = review(true, 'admission webhook "policy.example" denied the request')
    expect(html).toContain('This resource changed after your review')
    expect(html).toContain('denied the request')
  })

  it('says nothing extra when the resource did not change', () => {
    expect(review(false, 'forbidden')).not.toContain('changed after your review')
    expect(review(false, 'forbidden')).toContain('forbidden')
    expect(review()).not.toContain('changed after your review')
  })
})


it('retains pause context when the prediction removes the annotation without claiming confirmed resumption', () => {
  const baselineYaml = 'apiVersion: example.test/v1\nkind: Bucket\nmetadata:\n  name: archive\n  annotations:\n    crossplane.io/paused: "true"\nspec:\n  providerConfigRef:\n    name: default\n'
  const predictedYaml = baselineYaml.replace('    crossplane.io/paused: "true"', '    crossplane.io/paused: "false"')
  const result = renderToString(<YamlReview submittedYaml={predictedYaml} documents={[{ index: 0, status: 'accepted', action: 'update', baselineYaml, predictedYaml }]} onBack={() => {}} onApply={() => {}} />)
  expect(result).toContain('Pause affects reconciliation')
  expect(result).toContain('requests resumption')
  expect(result).not.toContain('will resume')
  expect(result.replaceAll('<!-- -->', '')).toContain('Server preview clears the pause request on Bucket archive')
})


it('shows pause context for an unselected later document because Apply submits the whole batch', () => {
  const first = 'apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: ordinary\n'
  const second = 'apiVersion: example.test/v1\nkind: Bucket\nmetadata:\n  name: later-paused\n  annotations:\n    crossplane.io/paused: "true"\nspec:\n  providerConfigRef:\n    name: default\n'
  const result = renderToString(<YamlReview submittedYaml={first + '\n---\n' + second} documents={[{ index: 0, status: 'accepted', predictedYaml: first }, { index: 1, status: 'accepted', predictedYaml: second }]} onBack={() => {}} onApply={() => {}} />)
  expect(result).toContain('Pause affects reconciliation')
  expect(result).toContain('Bucket later-paused')
})


it('states the actual retained pause in the prediction without inferring who preserved it', () => {
  const baselineYaml = 'apiVersion: example.test/v1\nkind: Bucket\nmetadata:\n  name: archive\n  annotations:\n    crossplane.io/paused: "true"\nspec:\n  providerConfigRef:\n    name: default\n'
  const submittedYaml = baselineYaml.replace('    crossplane.io/paused: "true"', '')
  const result = renderToString(<YamlReview submittedYaml={submittedYaml} documents={[{ index: 0, status: 'accepted', action: 'update', baselineYaml, predictedYaml: baselineYaml }]} onBack={() => {}} onApply={() => {}} />)
  expect(result.replaceAll('<!-- -->', '')).toContain('Server preview keeps the pause request on Bucket archive')
  expect(result).not.toContain('another field manager owns')
  expect(result).not.toContain('Server preview clears')
})

it.each(['apply', 'create'] as const)('handles a not-previewable Crossplane document in %s mode without pretending submitted YAML is live', mode => {
  const submittedYaml = 'apiVersion: example.test/v1\nkind: Bucket\nmetadata:\n  name: archive\nspec:\n  providerConfigRef:\n    name: default\n'
  const result = renderToString(<YamlReview submittedYaml={submittedYaml} mode={mode} documents={[{ index: 0, status: 'unavailable', submittedYaml }]} onBack={() => {}} onApply={() => {}} />)
  expect(result.includes('Existing pause state is unknown')).toBe(mode === 'apply')
  expect(result.includes('Omitting the annotation from server-side apply YAML does not guarantee its removal')).toBe(mode === 'apply')
  expect(result).not.toContain('Server preview clears')
})


it.each(['create', 'unavailable'] as const)('keeps per-document pause evidence paired when the first document has no baseline (%s)', firstState => {
  const firstYaml = 'apiVersion: example.test/v1\nkind: Bucket\nmetadata:\n  name: fresh\nspec:\n  providerConfigRef:\n    name: default\n'
  const pausedYaml = firstYaml.replace('name: fresh', 'name: archive\n  annotations:\n    crossplane.io/paused: "true"')
  const first = firstState === 'create'
    ? { index: 0, status: 'accepted' as const, action: 'create' as const, name: 'fresh', predictedYaml: firstYaml }
    : { index: 0, status: 'unavailable' as const, action: 'unknown' as const, name: 'fresh', submittedYaml: firstYaml }
  const result = renderToString(<YamlReview submittedYaml={firstYaml + '\n---\n' + pausedYaml} documents={[first, { index: 1, status: 'accepted', action: 'update', name: 'archive', baselineYaml: pausedYaml, predictedYaml: pausedYaml }]} onBack={() => {}} onApply={() => {}} />).replaceAll('<!-- -->', '')
  expect(result).toContain('Server preview keeps the pause request on Bucket archive')
  expect(result).not.toContain('Server preview clears the pause request')
  expect(result).not.toContain('Server preview requests a pause on Bucket archive')
  expect(result.includes('Live pause state is unverified for Bucket fresh')).toBe(firstState === 'unavailable')
})
