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
  spec: { replicas: 3, roles: ['broker'], storage: { type: 'jbod', volumes: [{ id: 0, type: 'persistent-claim', size: '100Gi', class: 'fast', deleteClaim: false }] }, resources: { requests: { cpu: '1', memory: '4Gi' }, limits: { memory: '8Gi' } } },
  status: { observedGeneration: 2, replicas: 2, roles: ['broker'], nodeIds: [0, 1], conditions: [{ type: 'Ready', status: 'True' }] },
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
  ]
  for (const [name, change, text, level] of cases) it(name, () => {
    const d = kafka(); change(d)
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
  })
  it('does not call a missing status healthy', () => {
    const d = kafka(); delete d.status
    const html = renderToStaticMarkup(<StrimziKafkaRenderer data={d} />)
    expect(html).toContain('Status unknown')
    expect(html).toContain('No node pools reported')
    expect(html).toContain('Not reported')
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
    for (const t of ['broker', '2 / 3', 'persistent-claim 100Gi (id 0, class fast)', 'Node IDs (2)', '1 / Not set', '4Gi / 8Gi', 'main', 'not itself a failure']) expect(html).toContain(t)
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
    expect(getStrimziClusterColumnValue(kafka(), 'kafkaListeners')).toBe('plain, external')
    expect(getStrimziClusterColumnValue(pool(), 'poolReplicas')).toBe('2 / 3')
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
