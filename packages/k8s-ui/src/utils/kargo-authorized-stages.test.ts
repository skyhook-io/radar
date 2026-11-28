import { describe, expect, it } from 'vitest'
import { KARGO_AUTHORIZED_STAGE_ANNOTATION, kargoAuthorizedStages } from './kargo-authorized-stages'

const app = (value?: string, apiVersion = 'argoproj.io/v1alpha1') => ({
  apiVersion,
  kind: 'Application',
  metadata: { annotations: value === undefined ? {} : { [KARGO_AUTHORIZED_STAGE_ANNOTATION]: value } },
})

describe('kargoAuthorizedStages', () => {
  it('reads one stage', () => {
    expect(kargoAuthorizedStages(app('kargo-demo:prod'))).toEqual([{ project: 'kargo-demo', stage: 'prod' }])
  })

  it('reads several stages, trimming and dropping repeats', () => {
    expect(kargoAuthorizedStages(app(' kargo-demo:uat , kargo-demo:prod,,kargo-demo:uat'))).toEqual([
      { project: 'kargo-demo', stage: 'uat' },
      { project: 'kargo-demo', stage: 'prod' },
    ])
  })

  it('is null without the annotation', () => {
    expect(kargoAuthorizedStages(app())).toBeNull()
    expect(kargoAuthorizedStages({ apiVersion: 'argoproj.io/v1alpha1', kind: 'Application', metadata: {} })).toBeNull()
  })

  it('is null when the Application could not be read', () => {
    expect(kargoAuthorizedStages(undefined)).toBeNull()
    expect(kargoAuthorizedStages(null)).toBeNull()
  })

  it.each(['prod', 'kargo-demo:', ':prod', 'kargo-demo:prod,uat', 'kargo-demo:*', '*:prod', ' , '])('rejects the whole malformed value %j, as Kargo does', (value) => {
    expect(kargoAuthorizedStages(app(value))).toBeNull()
  })

  it('ignores the annotation on anything but an Argo CD Application', () => {
    expect(kargoAuthorizedStages(app('kargo-demo:prod', 'example.com/v1'))).toBeNull()
    expect(kargoAuthorizedStages({ ...app('kargo-demo:prod'), kind: 'ApplicationSet' })).toBeNull()
  })
})
