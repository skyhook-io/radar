import type { QueryClient } from '@tanstack/react-query'
import type { CNPGDetailTarget } from './routes'

export function refetchCNPGDetail(client: QueryClient, target: CNPGDetailTarget): Promise<void> {
  return client.refetchQueries({
    type: 'active',
    predicate: ({ queryKey: key }) => {
      if (key[0] !== 'cnpg') return false
      if (['workspace', 'disk', 'fleet-metrics', 'operator-status'].includes(String(key[1]))) {
        return key[2] === '' || String(key[2]).split(',').includes(target.namespace)
      }
      if (key[1] === 'capabilities') return key[2] === target.plural && key[3] === target.namespace && key[4] === target.name
      if (key[1] === 'restore-capability') return key[2] === target.namespace
      return key[2] === target.namespace && key[3] === target.name
    },
  })
}
