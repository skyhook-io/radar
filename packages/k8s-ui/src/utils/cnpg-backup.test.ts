import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { cnpgBackupDeclaration, cnpgBackupDestinationBlocker, cnpgBarmanPlugin, cnpgDestinationPlugin, cnpgHasBackupDestination } from './cnpg-backup'

const cases: Array<{
  name: string
  cluster: Parameters<typeof cnpgBackupDeclaration>[0]
  method: string
  plugin: string
  hasDestination: boolean
  destinationPlugin: string
  blockerCode: string
  barman: { objectStore: string; serverName: string } | null
}> = JSON.parse(readFileSync(new URL('../../../../pkg/cnpg/testdata/backup-declarations.json', import.meta.url), 'utf8'))

describe('CNPG backup declarations shared with Go', () => {
  it.each(cases)('$name', (test) => {
    const declaration = cnpgBackupDeclaration(test.cluster)
    expect(cnpgHasBackupDestination(declaration)).toBe(test.hasDestination)
    expect(cnpgDestinationPlugin(declaration)?.name ?? '').toBe(test.destinationPlugin)
    expect(cnpgBackupDestinationBlocker(declaration, test.method || undefined, test.plugin)?.code ?? '').toBe(test.blockerCode)
    const barman = cnpgBarmanPlugin(declaration)
    if (test.barman) expect(barman).toMatchObject({ barmanObjectName: test.barman.objectStore || undefined, serverName: test.barman.serverName })
    else expect(barman).toBeUndefined()
  })
})
