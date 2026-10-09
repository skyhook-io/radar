import { expect, it } from 'vitest'
import { query, intervalQuery } from './CNPGClusterLogs'
it('requests all containers explicitly for snapshots, streams and bounded intervals', () => {
  expect(new URLSearchParams(query({}).slice(1)).get('container')).toBe('all')
  expect(new URLSearchParams(query({}, 50).slice(1)).get('container')).toBe('all')
  expect(new URLSearchParams(intervalQuery({}, 'from', 'to').slice(1)).get('container')).toBe('all')
  expect(new URLSearchParams(query({ container: 'plugin-barman-cloud' }).slice(1)).get('container')).toBe('plugin-barman-cloud')
  expect(new URLSearchParams(intervalQuery({ container: 'postgres' }, 'from', 'to').slice(1)).get('container')).toBe('postgres')
})
