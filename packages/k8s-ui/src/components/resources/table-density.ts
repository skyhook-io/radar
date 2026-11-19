// How dense the Resources table is: the padding of its cells and column
// headers, and of the toolbar row above it. One place, so column-width
// measuring (CELL_PADDING_PX, the header padding) and the rendered table
// can't disagree. Class names are literal so Tailwind sees them.
export const TABLE_DENSITY = {
  /** Body cells and column headers. */
  cellClass: 'px-3.5 py-2.5',
  /** cellClass's horizontal padding, both sides, in px. */
  cellPaddingX: 28,
  /** The toolbar row above the table, and its search input's vertical padding. */
  toolbarClass: 'px-4 py-2.5',
  searchClass: 'py-1.5',
}
