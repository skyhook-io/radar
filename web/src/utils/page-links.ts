/**
 * The label for "← back" on the page a push lands on: the title of the page
 * being left, which Radar keeps in the document title.
 */
export function currentPageLabel(): string {
  return document.title.replace(/\s*·\s*Radar$/, '') || 'previous page'
}

/**
 * Radar's Issues page narrowed to one subject (it has no link to a single issue).
 * The subject travels as `resource=ns/name`, never `namespace=`: App reads a bare
 * `namespace` as the view filter, and a URL without `namespaces` clears it.
 */
export function issuesPathForSubject(subject: { kind: string; group?: string; namespace: string; name: string }, viewNamespaces?: string | null): string {
  const params = new URLSearchParams()
  if (viewNamespaces) params.set('namespaces', viewNamespaces)
  params.set('kind', subject.kind)
  if (subject.group) params.set('group', subject.group)
  params.set('resource', subject.namespace ? `${subject.namespace}/${subject.name}` : subject.name)
  return `/issues?${params}`
}
