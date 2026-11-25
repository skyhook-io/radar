/**
 * Items in the order `order` lists their ids; unlisted ones follow, keeping
 * their own order. No order keeps them as they are.
 */
export function orderById<T extends { id: string }>(items: T[], order: readonly string[] | undefined): T[] {
  if (!order || order.length === 0) return items
  const rank = (id: string) => {
    const i = order.indexOf(id)
    return i < 0 ? order.length : i
  }
  return items
    .map((item, i) => ({ item, i }))
    .sort((a, b) => rank(a.item.id) - rank(b.item.id) || a.i - b.i)
    .map((x) => x.item)
}
