import { describe, expect, it } from 'vitest'
import { orderById } from './order-by-id'

describe('orderById', () => {
  it('puts listed ids first in their order, the rest after in theirs', () => {
    const items = ['a', 'b', 'c', 'd'].map((id) => ({ id }))
    expect(orderById(items, ['c', 'a']).map((x) => x.id)).toEqual(['c', 'a', 'b', 'd'])
    expect(orderById(items, undefined)).toBe(items)
  })
})
