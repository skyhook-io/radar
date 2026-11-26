import { createContext, useId, useLayoutEffect, useMemo, useRef, useState, type ReactNode, type Ref } from 'react';
import { ArrowRight, Boxes, ChevronDown, ChevronUp } from 'lucide-react';
import { disclosurePanelId } from '../ui/Collapse';
import type { AppRow } from '../../utils/applications';
import { ISSUE_SEVERITY_TEXT_CLASS, issueTitle } from './severity';

export const AppIssuePanelContext = createContext(false);

type IssueApp = Pick<AppRow, 'key' | 'name'>;
export type AppIssueLinkRenderer<T> = (appKey: string, label: ReactNode, leadIssue: T) => ReactNode;
type AppIssueListProps<T> = {
  /** Already filtered and sorted in triage order. */
  issues: readonly T[];
  issueKey: (issue: T) => string;
  appForIssue?: (issue: T) => IssueApp | undefined;
  /** Wrap the supplied app name and arrow in the host's detail link, or return null when unavailable. */
  renderAppLink?: AppIssueLinkRenderer<T>;
  /** The URL selection. A pinned issue can be outside the list and still reveal its app. */
  linkedIssueKey?: string;
  linkedAppKey?: string;
  /** Keep a manually opened detail visible if a refresh moves it into overflow. */
  openDetailKey?: string | null;
  renderIssue: (issue: T) => ReactNode;
};

type Group<T> = { app?: IssueApp; issues: T[] };
type DisplayIssue = { severity: string; category: string; reason?: string };

// Also used by IssuesView so viewport enrichment and expanded-row requests only
// see rows that the disclosure actually renders.
export function useAppIssueGroups<T>({ issues, issueKey, appForIssue, linkedIssueKey, linkedAppKey, openDetailKey }: Omit<AppIssueListProps<T>, 'renderIssue'>) {
  const ranked = useMemo(() => {
    const ordered: Group<T>[] = [];
    const apps = new Map<string, Group<T>>();
    for (const issue of issues) {
      const app = appForIssue?.(issue);
      const existing = app && apps.get(app.key);
      if (existing) existing.issues.push(issue);
      else {
        const group = { app, issues: [issue] };
        ordered.push(group);
        if (app) apps.set(app.key, group);
      }
    }
    return ordered.map(group => {
      // Four fit without a disclosure. For longer groups show three, so
      // opening a panel always reveals at least two additional issues.
      const preview = group.issues.length <= 4 ? group.issues : group.issues.slice(0, 3);
      return { ...group, preview, overflow: group.issues.slice(preview.length) };
    });
  }, [issues, appForIssue]);

  const linkedGroup = linkedAppKey ?? (linkedIssueKey && ranked.find(group =>
    group.issues.some(issue => issueKey(issue) === linkedIssueKey))?.app?.key);
  // Preserve the detail a reader opened before a refresh moved it into overflow.
  const activeGroup = openDetailKey && openDetailKey !== linkedIssueKey
    ? ranked.find(group => group.overflow.some(issue => issueKey(issue) === openDetailKey))?.app?.key
    : undefined;
  const [expanded, setExpanded] = useState(() => new Set<string>(linkedGroup ? [linkedGroup] : []));
  const [previousLink, setPreviousLink] = useState({ linkedIssueKey, linkedGroup });
  const [previousActiveGroup, setPreviousActiveGroup] = useState(activeGroup);
  if (previousActiveGroup !== activeGroup) {
    setPreviousActiveGroup(activeGroup);
    if (activeGroup && !expanded.has(activeGroup)) setExpanded(previous => new Set(previous).add(activeGroup));
  }
  if (previousLink.linkedIssueKey !== linkedIssueKey || (linkedGroup && previousLink.linkedGroup !== linkedGroup)) {
    setPreviousLink({ linkedIssueKey, linkedGroup });
    if (linkedGroup && !expanded.has(linkedGroup)) setExpanded(previous => new Set(previous).add(linkedGroup));
  }
  const groups = ranked.map(group => {
    const open = !!group.app && expanded.has(group.app.key);
    return { ...group, open, visible: open ? group.issues : group.preview };
  });
  const toggle = (key: string) => {
    setExpanded(previous => {
      const next = new Set(previous);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  };
  return { groups, toggle };
}

type Layout<T> = ReturnType<typeof useAppIssueGroups<T>>;

export function AppIssueRows<T extends DisplayIssue>({ layout, issueKey, renderIssue, renderAppLink, listRef, onCollapse }: {
  layout: Layout<T>;
  renderAppLink?: AppIssueLinkRenderer<T>;
  issueKey: (issue: T) => string;
  renderIssue: (issue: T) => ReactNode;
  listRef?: Ref<HTMLOListElement>;
  onCollapse?: (appKey: string, hidden: readonly T[]) => void;
}) {
  const id = useId();
  const focusAfterToggle = useRef<string | null>(null);
  useLayoutEffect(() => {
    if (!focusAfterToggle.current) return;
    const element = document.getElementById(focusAfterToggle.current);
    focusAfterToggle.current = null;
    const target = element?.querySelector<HTMLElement>('[aria-expanded]') ?? element;
    target?.focus({ preventScroll: true });
    target?.scrollIntoView?.({ block: 'nearest' });
  });
  const rows: ReactNode[] = [];
  // Draw the panel as adjoining list items. Keeping each issue in the same
  // key space preserves its DOM, keyboard focus and controls across regrouping.
  for (const { app, issues, visible, overflow, open } of layout.groups) {
    const panel = !!app && issues.length > 1;
    const headerId = panel ? disclosurePanelId('app-heading-' + id, app.key) : undefined;
    if (panel) {
      const link = renderAppLink?.(app.key, (
        <span title={`Open ${app.name} application`} className="inline-flex min-h-6 max-w-full items-center gap-1">
          <span className="min-w-0 break-words"><span className="sr-only">Open </span>{app.name}<span className="sr-only"> application</span></span>
          <ArrowRight aria-hidden className="h-3 w-3 shrink-0 text-theme-text-secondary" />
        </span>
      ), issues[0]);
      rows.push(
        <li key={`header:${app.key}`} role="presentation"
          className="flex min-w-0 items-center gap-2 rounded-t-xl border border-theme-border bg-theme-elevated px-4 py-2">
          <Boxes aria-hidden className="h-4 w-4 shrink-0 text-theme-text-secondary" />
          <h2 id={headerId} className="min-w-0 break-words text-sm font-semibold text-theme-text-primary [&_a]:rounded-sm [&_a:hover]:underline [&_a:focus-visible]:outline">{link ?? app.name}</h2>
          <span className="shrink-0 text-xs text-theme-text-secondary">· {issues.length} issues</span>
        </li>
      );
    }
    for (const issue of visible) {
      const key = issueKey(issue);
      const last = issue === visible[visible.length - 1] && !overflow.length;
      rows.push(
        <li key={`issue:${key}`} id={disclosurePanelId(id, key)} data-app-issue-row={key} tabIndex={-1} aria-describedby={headerId}
          className={panel ? `border-x border-b border-theme-border bg-theme-surface ${last ? 'mb-2 overflow-clip rounded-b-xl' : ''}` : 'mb-2'}>
          <AppIssuePanelContext.Provider value={panel}>{renderIssue(issue)}</AppIssuePanelContext.Provider>
        </li>
      );
    }
    if (!app || !overflow.length) continue;
    const critical = overflow.filter(issue => issue.severity === 'critical');
    const buttonId = disclosurePanelId('app-disclosure-' + id, app.key);
    const Chevron = open ? ChevronUp : ChevronDown;
    rows.push(
      <li key={`disclosure:${app.key}`} role="presentation"
        className="mb-2 overflow-clip rounded-b-xl border-x border-b border-theme-border bg-theme-surface">
        <button type="button" id={buttonId} aria-expanded={open}
          aria-controls={open ? overflow.map(issue => disclosurePanelId(id, issueKey(issue))).join(' ') : undefined}
          onClick={() => {
            focusAfterToggle.current = open ? buttonId : disclosurePanelId(id, issueKey(overflow[0]));
            if (open) onCollapse?.(app.key, overflow);
            layout.toggle(app.key);
          }}
          className="flex w-full items-center gap-2 px-4 py-2 text-left text-xs font-medium text-theme-text-secondary hover:bg-theme-hover focus-visible:outline focus-visible:-outline-offset-2">
          <Chevron aria-hidden className="h-3.5 w-3.5 shrink-0" />
          <span>{open ? 'Show fewer issues' : `Show ${overflow.length} more issues`}<span className="sr-only"> for {app.name}</span>
            {!open && critical.length > 0 && <span className={ISSUE_SEVERITY_TEXT_CLASS.critical}> · {critical.length} critical{critical.length === 1 && `: ${issueTitle(critical[0])}`}</span>}
          </span>
        </button>
      </li>
    );
  }
  return <ol ref={listRef} className="flex flex-col">{rows}</ol>;
}

/** Keep related issues together and disclose overflow only for longer app panels. */
export function AppIssueList<T extends DisplayIssue>(props: AppIssueListProps<T>) {
  const layout = useAppIssueGroups(props);
  return <AppIssueRows layout={layout} issueKey={props.issueKey} renderAppLink={props.renderAppLink} renderIssue={props.renderIssue} />;
}
