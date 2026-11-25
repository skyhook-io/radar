import { execFileSync, spawnSync } from 'node:child_process'
import { describe, expect, it } from 'vitest'
import { shellWord } from './shell'

const cases = [
  ['--namespace', '--namespace'],
  ['service/my-db:5432', 'service/my-db:5432'],
  ['user@host', 'user@host'],
  ['a=b', 'a=b'],
  ['', "''"],
  ['my database', "'my database'"],
  ["O'Reilly", "'O'\\''Reilly'"],
  ['=ls', "'=ls'"],
  ['~/db', "'~/db'"],
  ['$PATH', "'$PATH'"],
  ['one\ntwo', "'one\ntwo'"],
  ['one\\two', "'one\\two'"],
] as const

const zshCheck = spawnSync('zsh', ['--version'])
const zshAvailable = (zshCheck.error as NodeJS.ErrnoException | undefined)?.code !== 'ENOENT'

describe('shellWord', () => {
  it.each(cases)('quotes %j', (word, quoted) => {
    expect(shellWord(word)).toBe(quoted)
  })

  describe.each(['sh', 'zsh'])('%s round trip', (shell) => {
    it.runIf(shell === 'sh' || zshAvailable).each(cases)('preserves %j', (word) => {
      const output = execFileSync(shell, ['-c', `printf '%s' ${shellWord(word)}`], { encoding: 'utf8' })
      expect(output).toBe(word)
    })
  })
})
