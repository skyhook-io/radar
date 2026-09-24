import { describe, expect, test } from 'vitest'
import { destinationToast } from './destination-toast'

const ctx = {
  name: 'arn:aws:eks:eu-central-1:420859418125:cluster/eks-prod-cluster',
  cluster: 'arn:aws:eks:eu-central-1:420859418125:cluster/eks-prod-cluster',
  user: 'u',
  namespace: '',
  isCurrent: false,
}

describe('destinationToast', () => {
  test('offers to open it in the matching context', () => {
    const t = destinationToast('ConfigMap ingress-nginx/controller', { context: ctx, server: 'https://65b5.gr7.eu-central-1.eks.amazonaws.com', canSwitch: true })
    expect(t.message).toBe('ConfigMap ingress-nginx/controller is on eks-prod-cluster')
    expect(t.actionLabel).toBe('Open in eks-prod-cluster')
  })
  test('names the cluster without an action when switching is off', () => {
    const t = destinationToast('ConfigMap a/b', { context: ctx, server: 'https://65b5.gr7.eu-central-1.eks.amazonaws.com', canSwitch: false })
    expect(t.message).toContain('eks-prod-cluster')
    expect(t.actionLabel).toBeUndefined()
  })
  test('says which server is missing from the kubeconfig', () => {
    const t = destinationToast('ConfigMap a/b', { server: 'https://34.73.119.131', canSwitch: true })
    expect(t.detail).toContain('34.73.119.131')
    expect(t.actionLabel).toBeUndefined()
  })
  test('unknown destination', () => {
    const t = destinationToast('ConfigMap a/b', { canSwitch: true })
    expect(t.message).toBe('ConfigMap a/b is on another cluster')
    expect(t.actionLabel).toBeUndefined()
  })
})
