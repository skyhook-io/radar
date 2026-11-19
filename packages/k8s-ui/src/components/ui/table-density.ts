// How dense Radar's list tables are: the padding of their cells and column
// headers, and of the toolbar row above them. The Resources table, the
// sortable header (TH_CLASS) and the k8s-ui list views read it, so they stay
// one family, and the Resources table's column-width measuring
// (CELL_PADDING_PX, the header padding) can't disagree with what it renders.
// web/ list views (Helm, Upgrade readiness, Rightsizing) repeat the classes:
// radar-app supports k8s-ui releases older than this module. Class names are
// literal so Tailwind sees them.
export const TABLE_DENSITY = {
  /** Body cells and column headers. */
  cellClass: 'px-3.5 py-2.5',
  /** cellClass's vertical padding alone, for the compare and select columns. */
  cellYClass: 'py-2.5',
  /** cellClass's horizontal padding alone, for a list's column-header row. */
  cellXClass: 'px-3.5',
  /** cellClass's horizontal padding, both sides, in px. */
  cellPaddingX: 28,
  /** The toolbar row above the table, and its search input's vertical padding. */
  toolbarClass: 'px-4 py-2.5',
  searchClass: 'py-1.5',
}
