// How dense Radar's list tables are: the padding of their cells and column
// headers, and of the toolbar row above them. The Resources table, the
// sortable header (TH_CLASS) and the other list views read it, so they stay
// one family, and the Resources table's column-width measuring
// (CELL_PADDING_PX, the header padding) can't disagree with what it renders.
// Class names are literal so Tailwind sees them.
export const TABLE_DENSITY = {
  /** Body cells and column headers. */
  cellClass: 'px-3.5 py-2.5',
  /** cellClass's vertical padding alone, for the compare and select columns. */
  cellYClass: 'py-2.5',
  /** cellClass's horizontal padding, both sides, in px. */
  cellPaddingX: 28,
  /** The toolbar row above the table, and its search input's vertical padding. */
  toolbarClass: 'px-4 py-2.5',
  searchClass: 'py-1.5',
}
