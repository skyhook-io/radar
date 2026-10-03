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
