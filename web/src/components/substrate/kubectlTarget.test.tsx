import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { CommandChip, KubectlTargetContext, substrateKubectlTarget } from './shared'
import type { SubstrateConnection, SubstrateSettings } from '../../api/substrateWorkspace'
import { connection, managedConnection } from './substrate.fixtures'

const revert = 'kubectl ate revert actor lit-0193 -a research'
const target = { ...connection().target, context: 'dev-from-other-file', inFileName: 'dev', source: '/home/me/kube configs/staging' }

function show(view?: SubstrateConnection, activeContext = target.context) {
  return renderToStaticMarkup(
    <KubectlTargetContext.Provider value={substrateKubectlTarget(view, activeContext)}>
      <CommandChip command={revert} />
    </KubectlTargetContext.Provider>,
  ).replace(/&#x27;/g, "'")
}

function local(settings: SubstrateSettings = { identity: 'shared' }) {
  return connection({ target, saved: true, settings })
}

describe('copyable kubectl ate commands', () => {
  it('waits for the active context’s connection before showing a copy button', () => {
    expect(show()).toBe('')
    expect(show(local(), 'another-context')).toBe('')
  })

  it('pins a local command to the source file and the name inside that file', () => {
    const html = show(local())
    expect(html).toContain(`${revert} --kubeconfig '/home/me/kube configs/staging' --context dev`)
    expect(html).toContain('aria-label="Copy command"')
    expect(html).not.toContain('--endpoint')
  })

  it('includes a directly reachable local endpoint with standard cluster trust', () => {
    const html = show(local({ identity: 'tokenFile', tokenFile: '/home/me/ate.token', endpoint: '127.0.0.1:18443' }))
    expect(html).toContain('--endpoint 127.0.0.1:18443')
    expect(html).not.toContain('--token-file')
  })

  it.each([{ caFile: '/home/me/ca.pem' }, { serverName: 'ate.example.org' }])('hands off local custom TLS (%j)', (tls) => {
    const html = show(local({ identity: 'shared', endpoint: '127.0.0.1:18443', ...tls }))
    expect(html).toContain('127.0.0.1:18443')
    expect(html).toContain('with the CA and server name')
    expect(html).not.toContain('Copy command')
    expect(html).not.toContain('--endpoint')
  })

  it('hands off a local cluster DNS endpoint to a machine that can reach it', () => {
    const html = show(local({ identity: 'shared', endpoint: 'api.ate-system.svc:443' }))
    expect(html).toContain('resolve and reach api.ate-system.svc:443')
    expect(html).not.toContain('Copy command')
  })

  it('names the managed target without copying a cluster-internal endpoint flag', () => {
    const view = managedConnection()
    expect(show(view)).toContain('port-forwards to the ate-system/api Service')
    const html = show({ ...view, managed: { ...view.managed, endpoint: 'other.ate-system.svc:443' } })
    expect(html).toContain('other.ate-system.svc:443')
    expect(html).not.toContain('Copy command')
    expect(html).not.toContain('--endpoint')
  })
})
