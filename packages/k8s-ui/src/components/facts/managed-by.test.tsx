import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { ManagedByText, managedByLabel } from './managed-by'
import { cnpgManagedBy } from '../cnpg/workspace'

describe('ManagedByText', () => {
  it('names the GitOps manager and links it when its namespace is recorded', () => {
    const app = { kind: 'Application', group: 'argoproj.io', namespace: 'argocd', name: 'payments' }
    const html = renderToStaticMarkup(<ManagedByText refTo={app} onNavigate={() => {}} />)
    expect(html).toContain('Argo CD application')
    expect(html).toMatch(/<button[^>]*>argocd\/payments<\/button>/)
    expect(renderToStaticMarkup(<ManagedByText refTo={{ ...app, namespace: '' }} onNavigate={() => {}} />)).not.toContain('<button')
  })
  it('says nothing for a manager that is not a GitOps controller', () => {
    expect(renderToStaticMarkup(<ManagedByText refTo={{ kind: 'HelmRelease', namespace: 'db', name: 'pg' }} />)).toBe('')
    expect(managedByLabel({ kind: 'Kustomization', group: 'kustomize.toolkit.fluxcd.io', namespace: 'flux-system', name: 'apps' })).toBe('Flux Kustomization flux-system/apps')
  })
})

describe('cnpgManagedBy', () => {
  it('looks an object up by kind, namespace and name in the workspace answer', () => {
    const ws = { managedBy: { 'Cluster/db/pg': { kind: 'Application', group: 'argoproj.io', namespace: 'argocd', name: 'pg' } } }
    expect(cnpgManagedBy(ws, { kind: 'Cluster', metadata: { namespace: 'db', name: 'pg' } })?.name).toBe('pg')
    expect(cnpgManagedBy(ws, { kind: 'Pooler', metadata: { namespace: 'db', name: 'pg' } })).toBeUndefined()
  })
})
