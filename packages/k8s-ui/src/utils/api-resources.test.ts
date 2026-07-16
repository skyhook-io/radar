import { describe, expect, it } from 'vitest'
import { categorizeResources, findAPIResourceForRoute, formatGroupName, shortenGroupName } from './api-resources'

describe('findAPIResourceForRoute', () => {
  const resources = [
    { group: 'metrics.k8s.io', version: 'v1beta1', kind: 'PodMetrics', name: 'pods', namespaced: true, isCrd: false, verbs: ['get'] },
  ]

  it('prefers the canonical core resource when a discovered API has the same plural name', () => {
    expect(findAPIResourceForRoute(resources, 'pods')?.kind).toBe('Pod')
  })

  it('respects an explicit API group for a colliding resource', () => {
    expect(findAPIResourceForRoute(resources, 'pods', 'metrics.k8s.io')?.kind).toBe('PodMetrics')
  })

  it('resolves a discovered resource without a core collision', () => {
    const crd = { group: 'example.io', version: 'v1', kind: 'Widget', name: 'widgets', namespaced: true, isCrd: true, verbs: ['list'] }
    expect(findAPIResourceForRoute([crd], 'widgets')).toBe(crd)
  })

  it('resolves core resources before discovery loads', () => {
    expect(findAPIResourceForRoute(undefined, 'pods')?.kind).toBe('Pod')
  })

  it('falls back to an exact built-in API group before discovery loads', () => {
    expect(findAPIResourceForRoute(undefined, 'storageclasses', 'storage.k8s.io')).toMatchObject({
      kind: 'StorageClass',
      namespaced: false,
    })
  })

  it('supports the legacy Kind route form without weakening group matching', () => {
    expect(findAPIResourceForRoute(undefined, 'Pod')?.name).toBe('pods')
    expect(findAPIResourceForRoute(undefined, 'Pod', 'metrics.k8s.io')).toBeUndefined()
  })
})

describe('formatGroupName', () => {
  it('uses friendly names for common CRD groups seen in clusters', () => {
    expect(formatGroupName('policies.kyverno.io')).toBe('Kyverno')
    expect(formatGroupName('networking.gke.io')).toBe('GKE Networking')
    expect(formatGroupName('apiregistration.k8s.io')).toBe('API Registration')
    expect(formatGroupName('monitoring.googleapis.com')).toBe('Google Cloud Monitoring')
    expect(formatGroupName('sql.cnrm.cloud.google.com')).toBe('Config Connector')
    expect(formatGroupName('crd.k8s.amazonaws.com')).toBe('AWS VPC CNI')
    expect(formatGroupName('acid.zalan.do')).toBe('Zalando Postgres')
  })

  it('formats unmapped API groups without exposing raw domain strings', () => {
    expect(formatGroupName('widgets.example.io')).toBe('Example Widgets')
    expect(formatGroupName('api.my-company.dev')).toBe('My Company API')
    expect(formatGroupName('dns.gke.example.io')).toBe('Example DNS GKE')
    expect(formatGroupName('widgets.example.io')).not.toMatch(/\./)
  })

  it('does not promote Kubernetes domain-family suffixes into visible labels', () => {
    expect(formatGroupName('flowcontrol.apiserver.k8s.io')).toBe('Apiserver Flowcontrol')
    expect(formatGroupName('addons.cluster.x-k8s.io')).toBe('Cluster API')
    expect(formatGroupName('ipam.cluster.x-k8s.io')).toBe('Cluster API')
    expect(formatGroupName('nfd.k8s-sigs.io')).toBe('NFD')
  })

  it('keeps unmapped CRD groups separate from core categories', () => {
    expect(formatGroupName('networking.io')).toBe('Networking APIs')
    expect(formatGroupName('storage.io')).toBe('Storage APIs')
  })

  it('keeps shortenGroupName as the legacy suffix-stripping helper', () => {
    expect(shortenGroupName('networking.gke.io')).toBe('networking.gke')
    expect(shortenGroupName('rbac.authorization.k8s.io')).toBe('rbac.authorization')
    expect(shortenGroupName('widgets.example.dev')).toBe('widgets.example')
  })
})

describe('categorizeResources', () => {
  it('does not merge unmapped CRDs into matching core categories', () => {
    const categories = categorizeResources([
      { group: 'networking.io', version: 'v1', kind: 'WidgetRoute', name: 'widgetroutes', namespaced: true, isCrd: true, verbs: ['list'] },
    ])

    const networking = categories.find(c => c.name === 'Networking')
    const networkingAPIs = categories.find(c => c.name === 'Networking APIs')

    expect(networking?.resources.some(r => r.kind === 'Service')).toBe(true)
    expect(networking?.resources.some(r => r.kind === 'WidgetRoute')).toBe(false)
    expect(networkingAPIs?.resources.map(r => r.kind)).toEqual(['WidgetRoute'])
  })
})
