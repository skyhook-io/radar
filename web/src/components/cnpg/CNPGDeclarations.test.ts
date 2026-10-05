import { describe, expect, it } from 'vitest'
import { cnpgDeclarationBlocks, type DeclItem } from './CNPGDeclarations'

const item = (kind: DeclItem['kind'], pgName: string, database: string | undefined, state: DeclItem['state'] = 'applied'): DeclItem => ({
  key: `${kind}/${pgName}`,
  kind,
  pgName,
  database,
  state,
  resource: { kind, namespace: 'pg', name: pgName } as DeclItem['resource'],
  isField: false,
})

const items = [
  item('Database', 'demo_app', 'demo_app'),
  item('Publication', 'demo_app_pub', 'demo_app'),
  item('Subscription', 'demo_app_sub', 'demo_app', 'failed'),
  item('Database', 'demo_reporting', 'demo_reporting'),
  item('Publication', 'missing_pub', 'no_such_database', 'failed'),
  item('DatabaseRole', 'reader', undefined),
]

describe('cnpgDeclarationBlocks', () => {
  it('nests what lives in each database under it, undeclared databases after the declared ones, roles last', () => {
    const blocks = cnpgDeclarationBlocks(items, null)
    expect(blocks.map((b) => [b.database, b.head?.pgName, b.children.map((c) => c.pgName)])).toEqual([
      ['demo_app', 'demo_app', ['demo_app_pub', 'demo_app_sub']],
      ['demo_reporting', 'demo_reporting', []],
      ['no_such_database', undefined, ['missing_pub']],
      [undefined, undefined, ['reader']],
    ])
  })

  it('keeps a database as context for a matching item in it, and drops what does not match', () => {
    const blocks = cnpgDeclarationBlocks(items, 'failed')
    expect(blocks.map((b) => [b.database, b.head?.pgName, b.children.map((c) => c.pgName)])).toEqual([
      ['demo_app', 'demo_app', ['demo_app_sub']],
      ['no_such_database', undefined, ['missing_pub']],
    ])
  })
})
