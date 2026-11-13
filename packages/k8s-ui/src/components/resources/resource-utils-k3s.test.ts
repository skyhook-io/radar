import { describe, expect, it } from 'vitest'
import { hasCuratedColumns } from './ResourcesView'
import { getK3sHelmChartColumnValue, getK3sHelmChartStatus, k3sHelmChartSource, k3sHelmTargetNamespace, k3sValuesSummary } from './resource-utils-k3s'

const chart = (patch: any = {}) => ({
  apiVersion: 'helm.cattle.io/v1', kind: 'HelmChart',
  metadata: { name: 'traefik', namespace: 'kube-system' },
  spec: { chart: 'https://%{KUBERNETES_API}%/static/charts/traefik-37.1.1+up37.1.0.tgz', ...patch.spec },
  status: patch.status ?? { jobName: 'helm-install-traefik', conditions: [{ type: 'JobCreated', status: 'True' }, { type: 'Failed', status: 'False' }] },
})

describe('k3s HelmChart utils', () => {
  it('reads status, target namespace and bundled charts', () => {
    expect(getK3sHelmChartStatus(chart()).text).toBe('Job created')
    expect(getK3sHelmChartStatus(chart({ status: { conditions: [{ type: 'Failed', status: 'True', message: 'boom' }] } })).level).toBe('unhealthy')
    expect(getK3sHelmChartStatus(chart({ status: {} })).text).toBe('Not reported')
    expect(k3sHelmTargetNamespace(chart())).toBe('kube-system')
    expect(k3sHelmTargetNamespace(chart({ spec: { targetNamespace: 'apps' } }))).toBe('apps')
    expect(k3sHelmChartSource(chart())).toEqual({ chart: 'traefik-37.1.1+up37.1.0.tgz', bundled: true })
    expect(k3sHelmChartSource(chart({ spec: { chart: 'grafana', version: '8.0.0', repo: 'https://grafana.github.io/helm-charts' } }))).toEqual({ chart: 'grafana', version: '8.0.0', repo: 'https://grafana.github.io/helm-charts', bundled: false })
  })

  it('summarises values as counts and key names only', () => {
    expect(k3sValuesSummary({ valuesContent: 'a: 1\nb: 2\n', set: { 'x.y': 'SECRET' } })).toEqual({ lines: 2, setKeys: ['x.y'] })
    expect(k3sValuesSummary({})).toEqual({ lines: 0, setKeys: [] })
  })

  it('claims only helm.cattle.io columns', () => {
    expect(getK3sHelmChartColumnValue(chart(), 'chart')).toBe('traefik-37.1.1+up37.1.0.tgz')
    expect(getK3sHelmChartColumnValue(chart({ spec: { targetNamespace: 'apps' } }), 'targetNamespace')).toBe('apps')
    expect(getK3sHelmChartColumnValue({ ...chart(), apiVersion: 'source.toolkit.fluxcd.io/v1' }, 'chart')).toBeUndefined()
  })
})

it('keeps printer columns for the Flux HelmChart', () => {
  expect(hasCuratedColumns('helmcharts', 'source.toolkit.fluxcd.io')).toBe(false)
  expect(hasCuratedColumns('helmcharts', 'helm.cattle.io')).toBe(true)
})
