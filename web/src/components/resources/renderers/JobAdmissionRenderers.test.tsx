import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import { JobRenderer, JobSetRenderer } from './JobAdmissionRenderers'
vi.mock('../../execution/JobSetAdmission', () => ({ KueueAdmission: () => <div>admission evidence</div> }))
const job = { apiVersion: 'batch/v1', kind: 'Job', metadata: { name: 'training', namespace: 'ml' }, status: { conditions: [{ type: 'Failed', status: 'True', reason: 'BackoffLimitExceeded' }] } }
describe('admission renderer composition', () => {
 it('keeps Job failure above admission evidence', () => {
  const html = renderToStaticMarkup(<JobRenderer data={job} />)
  expect(html.indexOf('Job Issues')).toBeLessThan(html.indexOf('admission evidence'))
 })
 it('does not mount batch admission for a colliding Job kind', () => {
  expect(renderToStaticMarkup(<JobRenderer data={{...job, apiVersion:'batch.volcano.sh/v1alpha1'}} />)).not.toContain('admission evidence')
 })
 it('mounts admission in the JobSet drawer', () => {
  expect(renderToStaticMarkup(<JobSetRenderer data={{...job, apiVersion:'jobset.x-k8s.io/v1alpha2',kind:'JobSet'}} />)).toContain('admission evidence')
 })
})
