import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { CNPGLogicalPathView } from './CNPGLogicalPath'
import type { CNPGLogicalPath } from './logicalReplication'

const path: CNPGLogicalPath = {
  subscription: { namespace: 'db', name: 'sub', cluster: 'pg', applied: true },
  externalCluster: { name: 'upstream', declared: true },
  publisher: { kind: 'external', reason: 'the external cluster names no host' },
  publication: { name: 'pub' },
  slot: { name: 'sub' },
  failover: { text: 'unknown', tone: 'unknown' },
}

describe('CNPGLogicalPathView', () => {
  it('says unknown parts of the path in words, never as "?"', () => {
    const html = renderToStaticMarkup(<CNPGLogicalPathView path={path} />)
    expect(html).toContain('external cluster upstream')
    expect(html).toContain('database unknown')
    expect(html).not.toMatch(/upstream<\/span>\/\?|\/\?/)
  })
})
