import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { ResourceRendererDispatch, getResourceStatus } from '../../shared/ResourceRendererDispatch'
import { SecretProviderClassRenderer, SecretsStoreMountsSection } from './SecretsStoreRenderers'

const spc = (apiVersion = 'secrets-store.csi.x-k8s.io/v1') => ({
  apiVersion,
  kind: 'SecretProviderClass',
  metadata: { name: 'vault-database', namespace: 'default' },
  spec: {
    provider: 'vault',
    parameters: { vaultAddress: 'http://vault.vault:8200', roleName: 'database' },
    secretObjects: [{ secretName: 'db-creds-synced', type: 'Opaque', data: [{ key: 'password', objectName: 'db-password' }] }],
  },
})

function dispatch(data: any) {
  return renderToString(
    <ResourceRendererDispatch
      resource={{ kind: 'secretproviderclasses', namespace: 'default', name: 'vault-database', group: data.apiVersion.split('/')[0] }}
      data={data}
      onCopy={() => {}}
      copied={null}
      showCommonSections={false}
    />,
  )
}

const pod = (phase: string, className = 'vault-database') => ({
  metadata: { name: 'webapp', namespace: 'default', uid: 'pod-uid' },
  spec: {
    containers: [{ name: 'app', volumeMounts: [{ name: 'secrets-store', mountPath: '/mnt/secrets-store' }] }],
    volumes: [{ name: 'secrets-store', csi: { driver: 'secrets-store.csi.k8s.io', volumeAttributes: { secretProviderClass: className } } }],
  },
  status: { phase },
})

describe('SecretProviderClass dispatch', () => {
  it('renders provider and synced Secrets without provider parameters', () => {
    const html = dispatch(spc())
    expect(html).toContain('Secret Provider Class')
    expect(html).toContain('db-creds-synced')
    expect(html).not.toContain('vaultAddress')
  })

  it('reports no status of its own for a class', () => {
    expect(getResourceStatus('secretproviderclasses', spc())).toBeNull()
  })

  it('leaves a same-plural resource from another group to the generic renderer', () => {
    expect(dispatch(spc('secrets-store.example.com/v1'))).not.toContain('Secret Provider Class')
  })

  it('says when Pods could not be read instead of listing none', () => {
    const html = renderToString(<SecretProviderClassRenderer data={spc()} pods={{ state: 'forbidden' }} statuses={{ state: 'ok', items: [], complete: true }} />)
    expect(html).toContain('No permission to read Pods')
    expect(html).not.toContain('No Pod in this namespace')
  })
})

describe('SecretsStoreMountsSection', () => {
  it('renders nothing for a Pod without Secrets Store volumes', () => {
    const html = renderToString(<SecretsStoreMountsSection pod={{ spec: { volumes: [] } }} classes={{ state: 'loading' }} statuses={{ state: 'loading' }} />)
    expect(html).toBe('')
  })

  it('shows a missing class and the kubelet failure message', () => {
    const html = renderToString(<SecretsStoreMountsSection
      pod={pod('Pending', 'does-not-exist')}
      classes={{ state: 'ok', items: [spc()], complete: true }}
      statuses={{ state: 'ok', items: [], complete: true }}
      events={[{ reason: 'FailedMount', eventType: 'Warning', message: 'MountVolume.SetUp failed for volume "secrets-store" : SecretProviderClass "does-not-exist" not found', count: 3 }]}
    />)
    expect(html).toContain('not found in this namespace')
    expect(html).toContain('Mount failed')
    expect(html).toContain('×3')
  })

  it('shows mounted objects from the driver status', () => {
    const html = renderToString(<SecretsStoreMountsSection
      pod={pod('Running')}
      classes={{ state: 'ok', items: [spc()], complete: true }}
      statuses={{ state: 'ok', complete: true, items: [{
        metadata: { ownerReferences: [{ kind: 'Pod', uid: 'pod-uid' }] },
        status: { mounted: true, secretProviderClassName: 'vault-database', objects: [{ id: 'db-password', version: 'qxQU7TaN3RP7gILOMFYEn_46Cza7kCkhPxkrQuLXXB0=' }] },
      }] }}
    />)
    expect(html).toContain('Mounted')
    expect(html).toContain('db-password')
    expect(html).toContain('qxQU7TaN3RP7…')
    expect(html).toContain('vault')
  })
})
