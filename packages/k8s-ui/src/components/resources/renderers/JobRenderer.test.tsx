import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { JobRenderer } from './JobRenderer'

const job = { apiVersion: 'batch/v1', kind: 'Job', metadata: {}, spec: { suspend: true } }

describe('Job suspension banner', () => {
  it('keeps the ordinary suspension advice without Kueue evidence', () => {
    expect(renderToStaticMarkup(<JobRenderer data={job} />)).toContain('Pods will not be created until this Job is resumed.')
  })

  it('recognizes the queue label without a host lookup', () => {
    const html = renderToStaticMarkup(<JobRenderer data={{ ...job, metadata: { labels: { 'kueue.x-k8s.io/queue-name': 'quota' } } }} />)
    expect(html).toContain('Held by Kueue until admitted')
    expect(html).not.toContain('until this Job is resumed')
  })

  it('accepts controller-owned admission evidence from the host', () => {
    expect(renderToStaticMarkup(<JobRenderer data={job} kueueManaged />)).toContain('Held by Kueue until admitted')
  })

  it('does not treat a label on a colliding Job kind as Kueue evidence', () => {
    const html = renderToStaticMarkup(<JobRenderer data={{ ...job, apiVersion: 'batch.volcano.sh/v1alpha1', metadata: { labels: { 'kueue.x-k8s.io/queue-name': 'quota' } } }} />)
    expect(html).toContain('Job Suspended')
    expect(html).not.toContain('Held by Kueue')
  })

  it('preserves failures above admission and omits a hold banner when unsuspended', () => {
    const html = renderToStaticMarkup(<JobRenderer data={{ ...job, status: { conditions: [{ type: 'Failed', status: 'True', reason: 'DeadlineExceeded' }] } }} kueueManaged admissionContent={<p>admission evidence</p>} />)
    expect(html).toContain('Job Issues')
    expect(html.indexOf('Job Issues')).toBeLessThan(html.indexOf('admission evidence'))
    expect(html).not.toContain('Held by Kueue')
    expect(renderToStaticMarkup(<JobRenderer data={{ ...job, spec: { suspend: false } }} kueueManaged />)).not.toContain('Held by Kueue')
  })
})
