/**
 * The label for "← back" on the page a push lands on: the title of the page
 * being left, which Radar keeps in the document title.
 */
export function currentPageLabel(): string {
  return document.title.replace(/\s*·\s*Radar$/, '') || 'previous page'
}
