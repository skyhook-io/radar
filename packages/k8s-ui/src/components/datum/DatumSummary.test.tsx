import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { DatumSummary } from './DatumSummary'

describe('Datum summary fixture provenance', () => {
  it.each(['true', 'false', undefined])('shows a fixture banner only for a true synthetic annotation (%s)', value => {
    const html = renderToString(<DatumSummary object={{
      apiVersion: 'resourcemanager.miloapis.com/v1alpha1', kind: 'Project',
      metadata: { name: 'project', annotations: { 'radar.skyhook.io/synthetic-status': value } }, spec: {},
    }} />)
    expect(html.includes('Synthetic fixture status')).toBe(value === 'true')
  })
})
