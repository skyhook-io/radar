import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { UpgradePlanRenderer } from './UpgradePlanRenderer'

const CP = 'node-role.kubernetes.io/control-plane'
const data = {
  apiVersion: 'upgrade.cattle.io/v1', kind: 'Plan', metadata: { name: 'k3s-server', namespace: 'system-upgrade' },
  spec: { version: 'v1.32.1+k3s1', concurrency: 1, nodeSelector: { matchExpressions: [{ key: CP, operator: 'Exists' }] }, secrets: [{ name: 'creds', path: '/host' }],
    upgrade: { image: 'rancher/k3s-upgrade', envs: [{ name: 'TOKEN', value: 'ENV-SENTINEL' }], envFrom: [{ secretRef: { name: 'more' } }] },
    prepare: { image: 'rancher/k3s-upgrade', envs: [{ name: 'P', value: 'ENV-SENTINEL' }] } },
  status: { latestHash: 'h1', applying: ['n2'], conditions: [{ type: 'Validated', status: 'True' }] },
}
const nodes = [
  { name: 'n1', labels: { 'kubernetes.io/hostname': 'n1', [CP]: 'true', 'plan.upgrade.cattle.io/k3s-server': 'h1' } },
  { name: 'n2', labels: { 'kubernetes.io/hostname': 'n2', [CP]: 'true' } },
  { name: 'n3', labels: { 'kubernetes.io/hostname': 'n3', [CP]: 'true' } },
]
const text = (h: string) => h.replace(/<!-- -->/g, '')

describe('UpgradePlanRenderer', () => {
  it('shows progress and node rows when nodes are given', () => {
    const html = text(renderToString(<UpgradePlanRenderer data={data} nodes={nodes} />))
    expect(html).toContain('1 of 3 nodes upgraded')
    expect(html).toContain('n2')
    expect(html).toContain('Upgrading')
  })
  it('shows no per-node progress for a Plan the controller rejected', () => {
    const invalid = { ...data, status: { conditions: [{ type: 'Validated', status: 'False', message: 'bad drain' }] } }
    const html = text(renderToString(<UpgradePlanRenderer data={invalid} nodes={nodes} />))
    expect(html).toContain('No Node is upgraded while this Plan is not valid')
    expect(html).not.toContain('Pending')
    expect(html).not.toContain('nodes upgraded')
  })

  it('shows no progress bar when nodes could not be read', () => {
    const html = text(renderToString(<UpgradePlanRenderer data={data} nodes={null} />))
    expect(html).toContain('could not be read')
    expect(html).not.toContain('of 0')
    expect(text(renderToString(<UpgradePlanRenderer data={data} />))).not.toContain('nodes upgraded')
  })
  it('never renders env values', () => {
    const html = text(renderToString(<UpgradePlanRenderer data={data} nodes={nodes} />))
    expect(html).not.toContain('ENV-SENTINEL')
    expect(html).toContain('creds')
    expect(html).toContain('2 variables · from 1 source (values not shown)')
  })
})
