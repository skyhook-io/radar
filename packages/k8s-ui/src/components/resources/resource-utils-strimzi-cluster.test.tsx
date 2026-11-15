import { describe, it, expect } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { getGenericResourceStatus } from './generic-status'
import { getStrimziClusterStatus, getStrimziClusterColumnValue, getStrimziKafkaListeners, describeStrimziStorage } from './resource-utils-strimzi'
import { StrimziKafkaRenderer, StrimziKafkaNodePoolRenderer } from './renderers/StrimziKafkaRenderer'

const kafka = (): any => ({
  apiVersion: 'kafka.strimzi.io/v1', kind: 'Kafka',
  metadata: { name: 'main', namespace: 'kafka', generation: 3 },
  spec: { kafka: { version: '4.0.0', metadataVersion: '4.0-IV3', listeners: [
    { name: 'plain', port: 9092, type: 'internal', tls: false },
    { name: 'external', port: 9094, type: 'loadbalancer', tls: true, authentication: { type: 'scram-sha-512' } },
  ] } },
  status: {
    observedGeneration: 3, kafkaVersion: '4.0.0', clusterId: 'abc123', kafkaMetadataState: 'KRaft',
    kafkaNodePools: [{ name: 'brokers' }, { name: 'controllers' }],
    conditions: [{ type: 'Ready', status: 'True' }, { type: 'Warning', status: 'True', reason: 'KafkaStorage', message: 'storage-warning' }],
    listeners: [
      { name: 'plain', bootstrapServers: 'main-kafka-bootstrap.kafka.svc:9092', certificates: ['PEM-sentinel'] },
      { name: 'external', addresses: [{ host: 'lb.example.com', port: 9094 }] },
    ],
  },
})

const pool = (): any => ({
  apiVersion: 'kafka.strimzi.io/v1', kind: 'KafkaNodePool',
  metadata: { name: 'brokers', namespace: 'kafka', generation: 2, labels: { 'strimzi.io/cluster': 'main' } },
  spec: { replicas: 3, roles: ['broker'], storage: { type: 'jbod', volumes: [{ id: 0, type: 'persistent-claim', size: '100Gi', class: 'fast', deleteClaim: false }, { id: 1, type: 'persistent-claim', size: '50Gi', deleteClaim: true }] }, resources: { requests: { cpu: '1', memory: '4Gi' }, limits: { memory: '8Gi' } } },
  // Shaped like Strimzi 1.3's pool status: no Ready/NotReady, only Warning conditions.
  status: { observedGeneration: 2, replicas: 3, roles: ['broker'], nodeIds: [0, 1, 2], clusterId: 'abc123', labelSelector: 'strimzi.io/cluster=main', conditions: [] },
})

describe('getStrimziClusterStatus', () => {
  const cases: [string, (d: any) => void, string, string][] = [
    ['ready', () => {}, 'Ready', 'healthy'],
    ['not ready', d => { d.status.conditions = [{ type: 'NotReady', status: 'True' }] }, 'Not ready', 'degraded'],
    ['NotReady beats Ready', d => { d.status.conditions.push({ type: 'NotReady', status: 'True' }) }, 'Not ready', 'degraded'],
    ['no status', d => { delete d.status }, 'Status unknown', 'unknown'],
    ['stale', d => { d.metadata.generation = 4 }, 'Status stale', 'unknown'],
    ['paused annotation', d => { d.metadata.annotations = { 'strimzi.io/pause-reconciliation': 'true' } }, 'Reconciliation paused', 'neutral'],
    ['paused condition', d => { d.status.conditions = [{ type: 'ReconciliationPaused', status: 'True' }] }, 'Reconciliation paused', 'neutral'],
    ['terminating', d => { d.metadata.deletionTimestamp = '2026-01-01T00:00:00Z' }, 'Terminating', 'neutral'],
    ['no conditions', d => { d.status.conditions = [] }, 'Status unknown', 'unknown'],
    ['malformed condition', d => { d.status.conditions = [null, { type: 'Ready', status: 'True' }] }, 'Status unknown', 'unknown'],
    ['creating', d => { d.status = { conditions: [{ type: 'NotReady', status: 'True', reason: 'Creating' }] } }, 'Creating', 'unknown'],
    ['creating needs the Creating reason', d => { d.status = { conditions: [{ type: 'NotReady', status: 'True', reason: 'Timeout' }] } }, 'Status unknown', 'unknown'],
  ]
  for (const [name, change, text, level] of cases) it(name, () => {
    const d = kafka(); change(d)
    const s = getStrimziClusterStatus(d)
    expect([s.text, s.level]).toEqual([text, level])
    expect(getGenericResourceStatus(d)?.text).toBe(text)
  })
})

describe('getStrimziClusterStatus for KafkaNodePool', () => {
  const cases: [string, (d: any) => void, string, string][] = [
    ['accepted', () => {}, 'Accepted', 'neutral'],
    ['warning', d => { d.status.conditions = [{ type: 'Warning', status: 'True', reason: 'KafkaStorage' }] }, 'Warnings', 'degraded'],
    ['never reconciled', d => { d.status = null }, 'Not reconciled', 'unknown'],
    ['stale', d => { d.metadata.generation = 3 }, 'Status stale', 'unknown'],
    ['pool pause annotation is not honoured by Strimzi', d => { d.metadata.annotations = { 'strimzi.io/pause-reconciliation': 'true' } }, 'Accepted', 'neutral'],
    ['NotReady is not a pool condition', d => { d.status.conditions = [{ type: 'NotReady', status: 'True' }] }, 'Accepted', 'neutral'],
    // Strimzi's revertScaleDown / revertRoleChange keep the old allocation but stamp the current generation.
    ['scale-down reverted', d => { d.spec.replicas = 2 }, 'Not applied', 'degraded'],
    ['role removal reverted', d => { d.spec.roles = ['broker']; d.status.roles = ['broker', 'controller'] }, 'Not applied', 'degraded'],
    ['roles in a different order', d => { d.spec.roles = ['controller', 'broker']; d.status.roles = ['broker', 'controller'] }, 'Accepted', 'neutral'],
    ['stale wins over a reverted change', d => { d.spec.replicas = 2; d.metadata.generation = 3 }, 'Status stale', 'unknown'],
    ['terminating', d => { d.metadata.deletionTimestamp = '2026-01-01T00:00:00Z' }, 'Terminating', 'neutral'],
  ]
  for (const [name, change, text, level] of cases) it(name, () => {
    const d = pool(); change(d)
    const s = getStrimziClusterStatus(d)
    expect([s.text, s.level]).toEqual([text, level])
    expect(getGenericResourceStatus(d)?.text).toBe(text)
  })
})

describe('Kafka renderer', () => {
  it('shows version, pools, listeners and conditions without certificates', () => {
    const html = renderToStaticMarkup(<StrimziKafkaRenderer data={kafka()} />)
    for (const t of ['4.0.0', 'KRaft', 'abc123', 'brokers', 'controllers', 'Node Pools (2)', 'Listeners (2)', 'scram-sha-512', 'main-kafka-bootstrap.kafka.svc:9092', 'lb.example.com:9094', 'KafkaStorage', 'status.conditions in YAML']) expect(html).toContain(t)
    expect(html).not.toContain('PEM-sentinel')
    expect(html).not.toContain('storage-warning')
    expect(html).toContain('Reconciliation succeeded, but Strimzi flagged the configuration (KafkaStorage)')
  })
  it('shows no warning banner for a clean or failed Kafka', () => {
    const clean = kafka(); clean.status.conditions = [{ type: 'Ready', status: 'True' }]
    expect(renderToStaticMarkup(<StrimziKafkaRenderer data={clean} />)).not.toContain('Strimzi reported warnings')
    const failed = kafka(); failed.status.conditions = [{ type: 'NotReady', status: 'True' }, { type: 'Warning', status: 'True', reason: 'KafkaStorage' }]
    expect(renderToStaticMarkup(<StrimziKafkaRenderer data={failed} />)).not.toContain('Strimzi reported warnings')
  })
  it('does not call a missing status healthy', () => {
    const d = kafka(); delete d.status
    const html = renderToStaticMarkup(<StrimziKafkaRenderer data={d} />)
    expect(html).toContain('Status unknown')
    expect(html).toContain('empty when a reconciliation fails before reaching the pools')
    expect(html).toContain('at least one KafkaNodePool labelled strimzi.io/cluster=main')
    expect(html).toContain('Not reported')
  })
  it('names the brokers on the Kafka resource when no pools are used', () => {
    const d = kafka(); delete d.status.kafkaNodePools; d.spec.kafka.replicas = 3
    expect(renderToStaticMarkup(<StrimziKafkaRenderer data={d} />)).toContain('Brokers are declared on the Kafka resource')
  })
  it('keeps a failed upgrade\'s declared version beside the reported one', () => {
    const d = kafka(); d.spec.kafka.version = '4.1.0'
    expect(renderToStaticMarkup(<StrimziKafkaRenderer data={d} />)).toContain('4.0.0 (declared 4.1.0)')
    expect(getStrimziClusterColumnValue(d, 'kafkaVersion')).toBe('4.0.0 (declared 4.1.0)')
    delete d.status.kafkaVersion
    expect(renderToStaticMarkup(<StrimziKafkaRenderer data={d} />)).toContain('Not reported; declared 4.1.0')
    expect(getStrimziClusterColumnValue(d, 'kafkaVersion')).toBe('declared 4.1.0')
  })
  it('does not call a reported-only listener unauthenticated', () => {
    const d = kafka(); d.spec.kafka.listeners = [d.spec.kafka.listeners[0]]
    const external = getStrimziKafkaListeners(d).find(l => l.name === 'external')
    expect(external?.declared).toBe(false)
    expect(external?.authentication).toBeUndefined()
    const row = renderToStaticMarkup(<StrimziKafkaRenderer data={d} />).split('<tr').find(r => r.includes('>external<'))
    expect(row).toContain('Not declared')
    expect(row).not.toContain('>None<')
  })
  it('merges declared and reported listeners by name', () => {
    const l = getStrimziKafkaListeners(kafka())
    expect(l.map(x => x.name)).toEqual(['plain', 'external'])
    expect(l[1]).toMatchObject({ port: 9094, tls: true, authentication: 'scram-sha-512', addresses: ['lb.example.com:9094'] })
  })
})

describe('KafkaNodePool renderer', () => {
  it('shows roles, replicas, storage and resources', () => {
    const html = renderToStaticMarkup(<StrimziKafkaNodePoolRenderer data={pool()} />)
    for (const t of ['broker', '3 / 3', 'persistent-claim 100Gi (id 0, class fast)', 'persistent-claim 50Gi (id 1, PVC deleted once unused, including after scale-down)', 'Node IDs (3)', 'not the broker Pods', '1 / Not set', '4Gi / 8Gi', 'main', 'Accepted', 'reported on the Kafka resource']) expect(html).toContain(t)
    expect(html).not.toContain('Strimzi reported a warning')
  })
  it('shows a warning banner without the condition message', () => {
    const d = pool(); d.status.conditions = [{ type: 'Warning', status: 'True', reason: 'KafkaStorage', message: 'storage-sentinel' }]
    const html = renderToStaticMarkup(<StrimziKafkaNodePoolRenderer data={d} />)
    for (const t of ['Strimzi reported a warning', 'KafkaStorage']) expect(html).toContain(t)
    expect(html).not.toContain('storage-sentinel')
  })
  it('explains a reverted role change', () => {
    const d = pool(); d.spec.roles = ['broker']; d.status.roles = ['broker', 'controller']
    const html = renderToStaticMarkup(<StrimziKafkaNodePoolRenderer data={d} />)
    for (const t of ['Strimzi did not apply this change', 'ScaleDownPreventionCheck', 'broker, controller (declared broker)']) expect(html).toContain(t)
    expect(html).not.toContain('Strimzi reported a warning')
  })
  it('explains a pool Strimzi never reconciled', () => {
    const d = pool(); d.status = null
    expect(renderToStaticMarkup(<StrimziKafkaNodePoolRenderer data={d} />)).toContain('only while reconciling the Kafka named in the strimzi.io/cluster label')
  })
  it('flags a pool without its cluster label', () => {
    const d = pool(); delete d.metadata.labels
    expect(renderToStaticMarkup(<StrimziKafkaNodePoolRenderer data={d} />)).toContain('strimzi.io/cluster label missing')
  })
})

describe('columns', () => {
  it('derives Kafka and pool column values only for the exact group', () => {
    expect(getStrimziClusterColumnValue(kafka(), 'kafkaVersion')).toBe('4.0.0')
    expect(getStrimziClusterColumnValue(kafka(), 'kafkaPools')).toBe('brokers, controllers')
    const warned = kafka(); warned.status.conditions.push({ type: 'Warning', status: 'True', reason: 'KafkaStorage' }, { type: 'Warning', status: 'True' }, { type: 'Warning', status: 'False', reason: 'Cleared' })
    expect(getStrimziClusterStatus(warned).text).toBe('Ready')
    expect(getStrimziClusterColumnValue(warned, 'kafkaWarnings')).toBe('KafkaStorage, Warning')
    const clean = kafka(); clean.status.conditions = [{ type: 'Ready', status: 'True' }]
    expect(getStrimziClusterColumnValue(clean, 'kafkaWarnings')).toBe('')
    expect(getStrimziClusterColumnValue(kafka(), 'kafkaListeners')).toBe('plain, external')
    expect(getStrimziClusterColumnValue(pool(), 'poolReplicas')).toBe('3 / 3')
    expect(getStrimziClusterColumnValue(pool(), 'poolKafka')).toBe('main')
    expect(getStrimziClusterColumnValue({ ...kafka(), apiVersion: 'fake.kafka.strimzi.io/v1' }, 'kafkaVersion')).toBeUndefined()
    expect(getStrimziClusterColumnValue({ ...kafka(), apiVersion: 'other.io/v1' }, 'status')).toBeUndefined()
  })
})

describe('describeStrimziStorage', () => {
  it('handles ephemeral, missing and malformed storage', () => {
    expect(describeStrimziStorage({ type: 'ephemeral' })).toEqual(['ephemeral'])
    expect(describeStrimziStorage(undefined)).toEqual([])
    expect(describeStrimziStorage({ type: 'jbod', volumes: 'bad' })).toEqual([])
  })
})
