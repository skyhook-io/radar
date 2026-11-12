import { describe, expect, it } from 'vitest'
import { getUpgradePlanColumnValue, getUpgradePlanStatus, isDistributionUpgradePlan, upgradePlanProgress, upgradePlanTarget } from './resource-utils-upgrade-plan'
import { hasCuratedColumns } from './ResourcesView'

const CP = 'node-role.kubernetes.io/control-plane'
const LABEL = 'plan.upgrade.cattle.io/k3s-server'
const plan = (patch: any = {}) => ({
  apiVersion: 'upgrade.cattle.io/v1', kind: 'Plan', metadata: { name: 'k3s-server', namespace: 'system-upgrade' },
  spec: { version: 'v1.32.1+k3s1', concurrency: 1, nodeSelector: { matchExpressions: [{ key: CP, operator: 'Exists' }] }, upgrade: { image: 'rancher/k3s-upgrade' }, ...patch.spec },
  status: { latestHash: 'h1', applying: ['n2'], ...patch.status },
})
const nodes = [
  { name: 'n1', labels: { [CP]: 'true', [LABEL]: 'h1' } },
  { name: 'n2', labels: { [CP]: 'true' } },
  { name: 'n3', labels: { [CP]: 'true', [LABEL]: 'h0' } },
  { name: 'n4', labels: { 'node-role.kubernetes.io/worker': 'true' } },
]

describe('upgrade Plan utils', () => {
  it('derives node states from the plan hash and applying list', () => {
    const p = upgradePlanProgress(plan(), nodes)
    expect(p).toMatchObject({ state: 'ok', total: 3, upgraded: 1, upgrading: 1, pending: 1 })
    if (p.state !== 'ok') throw new Error('expected ok')
    expect(p.rows.map(r => `${r.name}:${r.state}`)).toEqual(['n2:upgrading', 'n3:pending', 'n1:upgraded'])
  })
  it('selects no node for a null selector and all for an empty one', () => {
    expect(upgradePlanProgress(plan({ spec: { nodeSelector: null } }), nodes)).toMatchObject({ state: 'ok', total: 0 })
    expect(upgradePlanProgress(plan({ spec: { nodeSelector: {} } }), nodes)).toMatchObject({ state: 'ok', total: 4 })
  })
  it('never counts a node upgraded without a latestHash', () => {
    expect(upgradePlanProgress(plan({ status: { latestHash: '', applying: [] } }), [{ name: 'n', labels: { [CP]: 'true', [LABEL]: '' } }])).toMatchObject({ upgraded: 0, pending: 1 })
  })
  it('reports an operator it cannot evaluate', () => {
    expect(upgradePlanProgress(plan({ spec: { nodeSelector: { matchExpressions: [{ key: 'x', operator: 'Gt', values: ['1'] }] } } }), nodes)).toEqual({ state: 'unsupported', operator: 'Gt' })
  })
  it('maps Validated, Complete and applying to one status', () => {
    const cond = (type: string, status: string) => ({ type, status })
    expect(getUpgradePlanStatus(plan({ status: { applying: [], conditions: [cond('Validated', 'False')] } })).text).toBe('Not valid')
    expect(getUpgradePlanStatus(plan({ status: { applying: ['n2'], conditions: [cond('Complete', 'True')] } })).text).toBe('In progress')
    expect(getUpgradePlanStatus(plan({ status: { applying: [], conditions: [cond('Complete', 'False')] } })).text).toBe('In progress')
    expect(getUpgradePlanStatus(plan({ status: { applying: [], conditions: [cond('Complete', 'True'), cond('Validated', 'True')] } })).text).toBe('Complete')
    expect(getUpgradePlanStatus(plan({ status: { applying: [] } })).text).toBe('Not reported')
  })
  it('recognises k3s and rke2 upgrader images only', () => {
    for (const image of ['rancher/k3s-upgrade:v1.32.1-k3s1', 'docker.io/rancher/rke2-upgrade@sha256:abc', 'registry.local:5000/mirror/k3s-upgrade']) {
      expect(isDistributionUpgradePlan(plan({ spec: { upgrade: { image } } }))).toBe(true)
    }
    expect(isDistributionUpgradePlan(plan({ spec: { upgrade: { image: 'rancher/system-agent:v0.3.13-suc' } } }))).toBe(false)
  })
  it('reads target and columns, and claims only its own group', () => {
    expect(upgradePlanTarget(plan())).toBe('v1.32.1+k3s1')
    expect(upgradePlanTarget(plan({ spec: { version: undefined, channel: 'https://update.k3s.io/v1-release/channels/stable' } }))).toBe('channel https://update.k3s.io/v1-release/channels/stable')
    expect(getUpgradePlanColumnValue(plan(), 'concurrency')).toBe('1')
    expect(getUpgradePlanColumnValue({ ...plan(), apiVersion: 'example.com/v1' }, 'version')).toBeUndefined()
    expect(hasCuratedColumns('plans', 'upgrade.cattle.io')).toBe(true)
    expect(hasCuratedColumns('plans', 'example.com')).toBe(false)
  })
})
