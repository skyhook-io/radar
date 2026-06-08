// Explicit exports (not `export *`) so the generic identity helpers stay
// module-internal and don't collide at the top-level barrel with the Checks
// queue's identically-named helpers when both land. Issue-prefixed public
// names are safe to surface.
export { IssueRow, IssuesView } from './IssuesView';
export type { IssueRowProps, IssueRowSlotContext, IssuesViewProps } from './IssuesView';
export {
  ISSUE_SEVERITIES,
  ISSUE_SEVERITY_RANK,
  isIssueSeverity,
  subjectRef,
  memberRef,
} from './types';
export type { Issue, IssueSeverity, IssueAffected, IssueResourceRef, IssueDiagnosticContext, IssueDiagnosticFact, IssueDiagnosticIssueRef, IssueDiagnosticRole, IssueChangeContext, IssueRecentChange, IssueRecentChangeField } from './types';
export {
  ISSUE_SEVERITY_LABEL,
  ISSUE_SEVERITY_BADGE_CLASS,
  ISSUE_SEVERITY_FILL_CLASS,
  ISSUE_SEVERITY_TEXT_CLASS,
  ISSUE_SEVERITY_RAIL_CLASS,
  groupBadgeClass,
  categoryLabel,
  groupLabel,
} from './severity';
