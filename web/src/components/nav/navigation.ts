// Sidebar navigation means “show me that destination.” An expanded investigation
// obscures it, while a docked investigation intentionally stays alongside it.
export function navigateFromPrimaryRail(
  expandedInvestigationOpen: boolean,
  closeDiagnose: () => void,
  navigate: () => void,
) {
  if (expandedInvestigationOpen) closeDiagnose()
  navigate()
}
