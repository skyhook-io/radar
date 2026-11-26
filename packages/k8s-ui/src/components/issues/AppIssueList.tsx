import { useId, useMemo, useState, type ReactNode, type Ref } from 'react';
import { CollapseChevron, disclosurePanelId } from '../ui/Collapse';
import type { AppRow } from '../../utils/applications';

type IssueApp = Pick<AppRow, 'key' | 'name'>;
type AppIssueListProps<T> = {
  /** Already filtered and sorted in triage order. */
  issues: readonly T[];
  issueKey: (issue: T) => string;
  appForIssue?: (issue: T) => IssueApp | undefined;
  /** The URL selection. A pinned issue can be outside the list and still reveal its app. */
  linkedIssueKey?: string;
  linkedAppKey?: string;
  renderIssue: (issue: T) => ReactNode;
};

type Group<T> = { app?: IssueApp; lead: T; more: T[] };

// Also used by IssuesView so viewport enrichment and expanded-row requests only
// see rows that the disclosure actually renders.
export function useAppIssueGroups<T>({ issues, issueKey, appForIssue, linkedIssueKey, linkedAppKey }: Omit<AppIssueListProps<T>, 'renderIssue'>, openDetailKey?: string | null) {
  const groups = useMemo(() => {
    const ordered: Group<T>[] = [];
    const apps = new Map<string, Group<T>>();
    for (const issue of issues) {
      const app = appForIssue?.(issue);
      const existing = app && apps.get(app.key);
      if (existing) existing.more.push(issue);
      else {
        const group = { app, lead: issue, more: [] };
        ordered.push(group);
        if (app) apps.set(app.key, group);
      }
    }
    return ordered;
  }, [issues, appForIssue]);

  const linkedGroup = linkedAppKey ?? (linkedIssueKey && groups.find(({ lead, more }) =>
    issueKey(lead) === linkedIssueKey || more.some(issue => issueKey(issue) === linkedIssueKey))?.app?.key);
  // If inventory arrives while someone is reading a row, keep it visible
  // when it becomes a child. Opening a lead row still shows just its detail.
  const activeGroup = openDetailKey && openDetailKey !== linkedIssueKey
    ? groups.find(({ more }) => more.some(issue => issueKey(issue) === openDetailKey))?.app?.key
    : undefined;
  const [expanded, setExpanded] = useState(() => new Set<string>(linkedGroup ? [linkedGroup] : []));
  const [previousLink, setPreviousLink] = useState({ linkedIssueKey, linkedGroup });
  const [previousActiveGroup, setPreviousActiveGroup] = useState(activeGroup);
  if (previousActiveGroup !== activeGroup) {
    setPreviousActiveGroup(activeGroup);
    if (activeGroup && !expanded.has(activeGroup)) setExpanded(new Set(expanded).add(activeGroup));
  }
  if (previousLink.linkedIssueKey !== linkedIssueKey || (linkedGroup && previousLink.linkedGroup !== linkedGroup)) {
    setPreviousLink({ linkedIssueKey, linkedGroup });
    if (linkedGroup && !expanded.has(linkedGroup)) setExpanded(new Set(expanded).add(linkedGroup));
  }
  const isOpen = (key: string) => expanded.has(key);
  const toggle = (key: string) => {
    setExpanded(previous => {
      const next = new Set(previous);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  };
  return { groups, isOpen, toggle };
}

type Layout<T> = ReturnType<typeof useAppIssueGroups<T>>;

export function AppIssueRows<T extends { severity: string }>({ layout, issueKey, renderIssue, listRef, onCollapse }: {
  layout: Layout<T>;
  issueKey: (issue: T) => string;
  renderIssue: (issue: T) => ReactNode;
  listRef?: Ref<HTMLOListElement>;
  onCollapse?: (appKey: string, hidden: readonly T[]) => void;
}) {
  const id = useId();
  const row = (issue: T) => <li key={issueKey(issue)} data-app-issue-row={issueKey(issue)}>{renderIssue(issue)}</li>;
  return (
    <ol ref={listRef} className="flex flex-col gap-1.5">
      {layout.groups.map(({ app, lead, more }) => {
        const groupKey = app ? `app:${app.key}` : `issue:${issueKey(lead)}`;
        const open = !!app && layout.isOpen(app.key);
        const panel = app && disclosurePanelId(id, app.key);
        const critical = more.filter(issue => issue.severity === 'critical').length;
        return (
          <li key={groupKey}>
            <div data-app-issue-row={issueKey(lead)}>{renderIssue(lead)}</div>
            {app && more.length > 0 && (
              <>
                <button type="button" aria-expanded={open} aria-controls={panel} onClick={() => {
                  if (open) onCollapse?.(app.key, more);
                  layout.toggle(app.key);
                }}
                  className="mt-1 inline-flex max-w-full items-center gap-1.5 rounded px-2 py-1 text-left text-xs text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary">
                  <CollapseChevron open={open} className="h-3.5 w-3.5" />
                  <span className="break-words">{open ? 'Show fewer issues' : `${more.length} more ${more.length === 1 ? 'issue' : 'issues'}`} for <strong className="font-medium">{app.name}</strong></span>
                  {!open && critical > 0 && <span className="shrink-0 text-theme-text-primary">· {critical} critical</span>}
                </button>
                <ol id={panel} hidden={!open} className={open ? 'mt-1 flex flex-col gap-1.5 border-l border-theme-border pl-3' : undefined}>
                  {open && more.map(row)}
                </ol>
              </>
            )}
          </li>
        );
      })}
    </ol>
  );
}

/** Fold an app's other issues under its highest-ranked row. Unattributed issues stay independent. */
export function AppIssueList<T extends { severity: string }>(props: AppIssueListProps<T>) {
  const layout = useAppIssueGroups(props);
  return <AppIssueRows layout={layout} issueKey={props.issueKey} renderIssue={props.renderIssue} />;
}
