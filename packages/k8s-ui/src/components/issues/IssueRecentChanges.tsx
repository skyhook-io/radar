import type { ReactNode } from 'react';
import { ArrowRight, History } from 'lucide-react';
import { CardSection, RadarUpgradeNote } from '../ui';
import { Tooltip } from '../ui/Tooltip';
import type { RadarUpgradeRequirement } from '../../types/fetch-error';
import { builtinGroupForKind } from '../../utils/api-resources';
import { formatCompactAge } from '../../utils/format';
import { apiVersionToGroup } from '../../utils/navigation';
import { englishPlural } from '../../utils/pluralize';
import { changeVerb } from './change-verb';
import type { Issue, IssueRecentChange, IssueResourceRef } from './types';

/** Where the host's fetch of the open issue's recent changes stands. */
export type IssueRecentChangesState =
  | { status: 'loading' }
  | { status: 'loaded' }
  /** The issue is no longer in the subject's answer: resolved, or it changed. */
  | { status: 'not_found' }
  /** Radar couldn't build the issue at all, so nothing about its changes is
   *  known: a native Helm release whose storage the caller may not read, or
   *  whose read failed. */
  | { status: 'unavailable'; reason: 'user_forbidden' | 'read_error' }
  | { status: 'failed' }
  /** The connected Radar predates the correlation endpoint. */
  | { status: 'unsupported'; upgrade: RadarUpgradeRequirement };

/** What a host supplies for an open row. Absent: the row renders from the list
 *  copy of the issue and shows no recent-changes section, as before. */
export interface IssueExpansion {
  /** A fresher copy of the open issue (same id) that carries its correlation.
   *  Replaces the list copy for the whole open row, timing included, since
   *  the server corrects timing alongside the correlation. */
  issue?: Issue;
  recentChanges: IssueRecentChangesState;
  onRetry?: () => void;
  retrying?: boolean;
  /** Set when the latest refresh failed and `issue` is the last answer that
   *  arrived; `fetchedAt` is when it did (epoch ms). */
  refreshFailed?: { fetchedAt: number };
}

// Changes up to this long after first_seen still count as "before": first_seen
// is the earliest evidence of the failing state, which can trail the change
// that preceded it by the time a Pod takes to start and fail.
const FIRST_SEEN_SLACK_MS = 30_000;

export interface RecentChangeGroups {
  /** Grouped around first_seen. Null when there is no first_seen, or a grouped
   *  issue's onsets are mixed (some members known, some not). */
  before: IssueRecentChange[] | null;
  after: IssueRecentChange[] | null;
  /** Ungrouped, newest first. Set exactly when before/after are null. */
  all: IssueRecentChange[] | null;
}

function changeTime(change: IssueRecentChange): number {
  const t = Date.parse(change.timestamp);
  return Number.isFinite(t) ? t : 0;
}

/** First-seen anchor, or null when there is none to group around. An unknown
 *  onset still groups: the headings are about when the issue was first seen,
 *  not when it began. */
export function recentChangesAnchor(issue: Issue): number | null {
  const coverage = issue.onset_coverage;
  const mixed = (coverage?.known ?? 0) > 0 && (coverage?.unknown ?? 0) > 0;
  if (!issue.first_seen || mixed) return null;
  const t = Date.parse(issue.first_seen);
  return Number.isFinite(t) ? t : null;
}

export function groupRecentChanges(issue: Issue): RecentChangeGroups {
  const changes = issue.correlated_changes ?? [];
  const anchor = recentChangesAnchor(issue);
  const newestFirst = [...changes].sort((a, b) => changeTime(b) - changeTime(a));
  if (anchor === null) return { before: null, after: null, all: newestFirst };
  return {
    before: newestFirst.filter((c) => changeTime(c) <= anchor + FIRST_SEEN_SLACK_MS),
    after: newestFirst.filter((c) => changeTime(c) > anchor + FIRST_SEEN_SLACK_MS),
    all: null,
  };
}

function span(ms: number): string {
  const minutes = Math.max(1, Math.round(Math.abs(ms) / 60_000));
  if (minutes < 60) return `${minutes} min`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `${hours} h`;
  return `${Math.round(hours / 24)} d`;
}

/** How long ago a change happened ("4m ago"). The collapsed row's note and an
 *  ungrouped Recent changes row both use it, so they never disagree. */
export function changeAgo(change: IssueRecentChange, now = Date.now()): string {
  return `${formatCompactAge(change.timestamp, now)} ago`;
}

/** "4 min earlier" / "6 min later" against first_seen, or "12m ago" against now. */
export function changeTimeLabel(change: IssueRecentChange, anchor: number | null, now = Date.now()): string {
  if (anchor === null) return changeAgo(change, now);
  const t = changeTime(change);
  const delta = t - anchor;
  if (Math.abs(delta) < 60_000) return 'within a minute of first seen';
  return delta < 0 ? `${span(delta)} earlier` : `${span(delta)} later`;
}

function clockTime(iso: string | undefined): string | null {
  if (!iso) return null;
  const t = Date.parse(iso);
  if (!Number.isFinite(t)) return null;
  const date = new Date(t);
  const sameDay = date.toDateString() === new Date().toDateString();
  return sameDay
    ? date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
    : date.toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
}

function kindList(kinds: { group?: string; kind: string }[] | undefined): string | null {
  const names = [...new Set((kinds ?? []).map((ref) => ref.kind).filter(Boolean).map(englishPlural))];
  if (names.length === 0) return null;
  if (names.length === 1) return names[0];
  return `${names.slice(0, -1).join(', ')} and ${names[names.length - 1]}`;
}

// complete_since is the boundary for every kind the answer depends on (a kind
// can start being watched after Radar does), and only callers who read every
// namespace get it. Without it, the line names no time rather than Radar's
// own start, which can be earlier than the gap.
function historyLine(reason: string, completeSince: string | undefined): string | null {
  if (reason === 'observation_started_late') {
    const since = clockTime(completeSince);
    return since
      ? `Radar may have missed changes made before ${since}.`
      : 'Radar may have missed some earlier changes.';
  }
  if (reason === 'events_evicted') {
    const since = clockTime(completeSince);
    return since
      ? `Radar no longer holds all its history from before ${since}, so it can't confirm nothing changed.`
      : "Radar no longer holds all its older history, so it can't confirm nothing changed.";
  }
  return null;
}

function isNativeHelm(issue: Issue): boolean {
  return issue.kind === 'HelmRelease' && issue.group === 'helm.sh';
}

// Kinds whose directly referenced ConfigMaps and Secrets the correlation also
// reads (the server's workload kinds).
const WORKLOAD_KINDS = new Set(['Deployment', 'StatefulSet', 'DaemonSet', 'Rollout', 'Pod']);

/** "this Deployment", "this Helm release": how the section names the issue's own subject. */
function thisSubject(issue: Issue): string {
  return isNativeHelm(issue) ? 'this Helm release' : `this ${issue.kind}`;
}

/** The server checked only the policy that rejected this admission issue:
 *  Radar doesn't record changes to its subject kind (a Job, a bare ReplicaSet). */
export function policyOnlyAdmission(issue: Issue): boolean {
  return issue.no_recent_changes?.scope === 'admission_policy';
}

function rejectingPolicy(issue: Issue): string | null {
  if (issue.category === 'quota_exceeded') return 'the ResourceQuota or LimitRange';
  if (issue.category === 'admission_webhook_blocking') return 'the webhook configuration';
  return null;
}

/** What a "no changes" answer covered, derived from the issue the server
 *  correlated: the subject (when its changes are recorded), a workload's
 *  ConfigMaps and Secrets, and for an admission rejection the kind of policy
 *  that rejected it. */
export function noChangesScope(issue: Issue): string {
  const subject = thisSubject(issue);
  // Naming the policy is safe: when the server can't identify it, it sends
  // sources_unresolved instead of no_recent_changes, so no "no changes" line
  // names a policy that wasn't read.
  const policy = rejectingPolicy(issue);
  if (policyOnlyAdmission(issue)) return `${policy ?? 'the policy'} that rejected ${subject}`;
  if (policy) return `${subject} or ${policy} that rejected it`;
  if (WORKLOAD_KINDS.has(issue.kind)) return `${subject} or its ConfigMaps and Secrets`;
  return subject;
}

function isSubjectChange(issue: Issue, change: IssueRecentChange): boolean {
  if (change.kind !== issue.kind || change.name !== issue.name || (change.namespace ?? '') !== (issue.namespace ?? '')) return false;
  if (isHelmSource(change)) return isNativeHelm(issue);
  return !change.apiVersion || apiVersionToGroup(change.apiVersion) === (issue.group ?? '');
}

function isHelmSource(change: IssueRecentChange): boolean {
  return change.source === 'helm' || (change.kind === 'HelmRelease' && !change.apiVersion);
}

function unknownLine(reason: string, kindsText: string | null): string {
  switch (reason) {
    case 'user_forbidden':
      return kindsText
        ? `You don't have access to ${kindsText} here, so Radar can't confirm nothing changed.`
        : "You don't have access to everything this issue depends on, so Radar can't confirm nothing changed.";
    case 'user_permission_check_failed':
      return kindsText ? `Radar couldn't check your access to ${kindsText}.` : "Radar couldn't check your access here.";
    case 'radar_kind_not_collected':
    case 'radar_namespace_not_collected':
      return kindsText ? `Radar isn't watching ${kindsText} here.` : "Radar isn't watching everything this issue depends on here.";
    case 'kind_sync_pending':
      return kindsText ? `Radar is still loading ${kindsText}.` : 'Radar is still loading this data.';
    case 'sources_unresolved':
      return "Radar couldn't tell which ConfigMaps and Secrets this workload uses, so it can't confirm nothing changed.";
    case 'history_incomplete':
      return "Radar doesn't hold all its history for this time, so it can't confirm nothing changed.";
    default:
      return "Radar can't confirm nothing changed.";
  }
}

function isAdmissionIssue(issue: Issue): boolean {
  return issue.category === 'quota_exceeded' || issue.category === 'admission_webhook_blocking';
}

/** sources_unresolved: which source Radar couldn't identify, from the kinds
 *  the server names and the issue kind. */
function unresolvedSourcesLine(issue: Issue, kinds: { group?: string; kind: string }[] | undefined): string {
  const names = new Set((kinds ?? []).map((ref) => ref.kind));
  if (names.has('MutatingWebhookConfiguration') || names.has('ValidatingWebhookConfiguration')) {
    return "Radar couldn't tell which webhook configuration rejected this, so it can't confirm nothing changed.";
  }
  if (names.has('ResourceQuota')) {
    return "Radar couldn't find the ResourceQuota that rejected this, so it can't confirm nothing changed.";
  }
  if (isAdmissionIssue(issue) && names.size === 0) {
    return "Radar couldn't tell which policy rejected this, so it can't confirm nothing changed.";
  }
  return unknownLine('sources_unresolved', null);
}

/** Every line explaining why the absence of changes can't be claimed. */
export function recentChangesLimits(issue: Issue): { lines: { reason: string; text: string }[]; readError: boolean } {
  const correlation = issue.correlation;
  const lines: { reason: string; text: string }[] = [];
  const unknown = correlation?.unknown;
  // History lines only when the server says history limits this answer: a
  // correlation can carry history coverage while every source was read in full.
  const history = unknown?.reason === 'history_incomplete' ? correlation?.history : undefined;
  if (history?.incomplete) {
    for (const reason of new Set(history.reasons ?? [])) {
      const text = historyLine(reason, history.complete_since);
      if (text) lines.push({ reason, text });
    }
  }
  const readError = unknown?.reason === 'read_error';
  if (unknown && !readError && !(unknown.reason === 'history_incomplete' && lines.length > 0)) {
    const text = unknown.reason === 'sources_unresolved'
      ? unresolvedSourcesLine(issue, unknown.kinds)
      : unknownLine(unknown.reason, kindList(unknown.kinds));
    lines.push({ reason: unknown.reason, text });
  }
  return { lines, readError };
}

/** Where a change's "View on Timeline" link goes: the timeline event it was
 *  read from. */
export interface IssueTimelineEventTarget {
  cluster_id?: string;
  eventId: string;
  /** The changed object's namespace; empty for a cluster-scoped object. */
  namespace: string;
  /** When the change happened (RFC 3339), so the Timeline can show that time. */
  timestamp: string;
}

/** The Timeline target for a change read from one timeline event, or null
 *  (a native Helm release record has none). */
export function recentChangeTimelineTarget(issue: Issue, change: IssueRecentChange): IssueTimelineEventTarget | null {
  if (!change.event_id) return null;
  return { cluster_id: issue.cluster_id, eventId: change.event_id, namespace: change.namespace ?? '', timestamp: change.timestamp };
}

/** How a host opens a change's timeline event. Omit both to keep each change's
 *  link on the changed object. */
export interface IssueTimelineLinks {
  timelineEventHref?: (target: IssueTimelineEventTarget) => string;
  /** In-app navigation; takes precedence over timelineEventHref. */
  onTimelineEventClick?: (target: IssueTimelineEventTarget) => void;
  /** Whether the host's Timeline can show this event (it may be held to
   *  namespaces that exclude a cluster-scoped one). A change it can't show
   *  keeps its object link. Omit when every event can be shown. */
  canOpenTimelineEvent?: (target: IssueTimelineEventTarget) => boolean;
}

/** The object a change row links to, or null when it can't be opened. */
export function recentChangeRef(issue: Issue, change: IssueRecentChange): IssueResourceRef | null {
  if (change.changeType === 'delete') return null;
  const ref = { cluster_id: issue.cluster_id, kind: change.kind, namespace: change.namespace ?? '', name: change.name };
  if (isHelmSource(change)) {
    return { ...ref, group: 'helm.sh' };
  }
  const group = change.apiVersion ? apiVersionToGroup(change.apiVersion) : builtinGroupForKind(change.kind);
  return group === undefined ? null : { ...ref, group };
}

function hasCorrelation(issue: Issue): boolean {
  return Boolean(issue.correlation || issue.correlated_changes || issue.no_recent_changes);
}

function noChangesShown(issue: Issue): boolean {
  return !(issue.correlated_changes?.length) && Boolean(issue.no_recent_changes) && !issue.correlated_changes_incomplete;
}

/** Whether the Recent changes section renders for this row. Every fetch
 *  state but a loaded answer shows a line. A loaded answer shows nothing for
 *  an issue Radar doesn't correlate, or one with no changes, no absence claim
 *  and no reason it can't make one: never a heading over nothing. */
export function recentChangesSectionShown(issue: Issue, expansion: IssueExpansion): boolean {
  if (expansion.recentChanges.status !== 'loaded') return true;
  if (!hasCorrelation(issue)) return false;
  const { lines, readError } = recentChangesLimits(issue);
  return Boolean(issue.correlated_changes?.length) || noChangesShown(issue) || Boolean(expansion.refreshFailed) ||
    Boolean(issue.correlated_changes_more_not_shown) || Boolean(issue.correlated_changes_incomplete) || readError || lines.length > 0;
}

/** Whether the section takes over from the "Changed N ago" line under What's
 *  wrong: it shows Radar's correlation for this issue, or is fetching it. On a
 *  Radar too old to correlate, a failed or refused fetch, or an issue that
 *  changed since the list loaded, the line stays, so nothing is lost. */
export function recentChangesReplaceChangeContext(issue: Issue, expansion: IssueExpansion | undefined): boolean {
  if (!expansion) return false;
  const status = expansion.recentChanges.status;
  return (status === 'loading' || status === 'loaded') && recentChangesSectionShown(issue, expansion);
}

const NOTE_CLASS = 'text-xs leading-relaxed text-theme-text-secondary';
const RETRY_CLASS = 'ml-1 text-xs font-medium text-accent-text hover:underline disabled:opacity-60';

const LINK_CLASS = 'min-w-0 truncate font-medium text-[var(--color-radar-accent,var(--accent))] hover:underline';
const TIMELINE_LINK_CLASS = 'inline-flex items-center gap-1 whitespace-nowrap text-xs font-medium text-[var(--color-radar-accent,var(--accent))] hover:underline';

function ChangeObject({
  issue,
  change,
  resourceHref,
  onResourceClick,
}: {
  issue: Issue;
  change: IssueRecentChange;
  /** Omit both to name the object without linking it. */
  resourceHref?: (ref: IssueResourceRef) => string;
  onResourceClick?: (ref: IssueResourceRef) => void;
}) {
  if (isSubjectChange(issue, change)) {
    const label = thisSubject(issue);
    return <span className="min-w-0 truncate text-theme-text-secondary">{label.charAt(0).toUpperCase() + label.slice(1)}</span>;
  }
  const ref = recentChangeRef(issue, change);
  const name = change.namespace && change.namespace !== issue.namespace ? `${change.namespace} / ${change.name}` : change.name;
  const kind = <span className="shrink-0 font-mono text-[11px] uppercase tracking-wide text-theme-text-tertiary">{isHelmSource(change) ? 'Helm release' : change.kind}</span>;
  let target: ReactNode = <span className="min-w-0 truncate font-medium text-theme-text-primary">{name}</span>;
  if (ref && onResourceClick) {
    target = <button type="button" className={`${LINK_CLASS} text-left`} onClick={() => onResourceClick(ref)}>{name}</button>;
  } else if (ref && resourceHref) {
    target = <a href={resourceHref(ref)} className={LINK_CLASS}>{name}</a>;
  }
  return (
    <span className="flex min-w-0 items-baseline gap-1.5">
      {kind}
      {target}
    </span>
  );
}

function ChangeRow({
  issue,
  change,
  anchor,
  resourceHref,
  onResourceClick,
  timelineEventHref,
  onTimelineEventClick,
  canOpenTimelineEvent,
}: {
  issue: Issue;
  change: IssueRecentChange;
  anchor: number | null;
  resourceHref?: (ref: IssueResourceRef) => string;
  onResourceClick?: (ref: IssueResourceRef) => void;
} & IssueTimelineLinks) {
  const subjectName = `${issue.kind}/${issue.name}`;
  const referencedBySubject = change.consumed_by?.includes(subjectName);
  const otherReferences = (change.consumed_by ?? []).filter((r) => r !== subjectName);
  const summary = change.summary && change.summary !== change.changeType ? change.summary : null;
  // Helm rows carry synthetic field names; they say nothing a reader can use.
  const paths = isHelmSource(change) ? [] : (change.fields ?? []).slice(0, 5).map((field) => field.path);
  const exact = new Date(changeTime(change)).toLocaleString();
  const references = referencedBySubject
    ? `Referenced by ${thisSubject(issue)}`
    : otherReferences.length > 0 ? `Referenced by ${otherReferences.join(', ')}` : null;
  const shownPaths = !summary && paths.length > 0 ? paths.join(', ') : null;
  // A change read from one timeline event links to that event; the object is
  // then named, not linked, so the row has one way out. A change with no
  // event (a Helm release record) keeps the link to the object.
  const candidate = onTimelineEventClick || timelineEventHref ? recentChangeTimelineTarget(issue, change) : null;
  const target = candidate && (!canOpenTimelineEvent || canOpenTimelineEvent(candidate)) ? candidate : null;
  let timelineLink: ReactNode = null;
  if (target) {
    const label = (
      <>
        View on Timeline
        <ArrowRight className="h-3 w-3 shrink-0" aria-hidden />
      </>
    );
    timelineLink = onTimelineEventClick ? (
      <button type="button" className={TIMELINE_LINK_CLASS} onClick={() => onTimelineEventClick(target)} data-change-link="timeline">{label}</button>
    ) : (
      <a href={timelineEventHref!(target)} className={TIMELINE_LINK_CLASS} data-change-link="timeline">{label}</a>
    );
  }
  return (
    <li className="flex min-w-0 items-start gap-3" data-change-row>
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <div className="flex min-w-0 items-baseline gap-2 text-sm">
          <span className="shrink-0 font-medium text-theme-text-primary">{changeVerb(change)}</span>
          {target ? (
            <ChangeObject issue={issue} change={change} />
          ) : (
            <ChangeObject issue={issue} change={change} resourceHref={resourceHref} onResourceClick={onResourceClick} />
          )}
        </div>
        {summary || shownPaths || references ? (
          <p className="min-w-0 break-words text-xs leading-relaxed text-theme-text-secondary">
            {summary ? <span data-change-detail="summary">{summary}</span> : null}
            {shownPaths ? <span className="font-mono" data-change-detail="paths">{shownPaths}</span> : null}
            {(summary || shownPaths) && references ? ' · ' : null}
            {references ? <span data-change-detail="references">{references}</span> : null}
          </p>
        ) : null}
      </div>
      <div className="flex shrink-0 flex-col items-end gap-0.5">
        <Tooltip
          content={summary && paths.length > 0 ? (
            <>
              <span className="block">{exact}</span>
              <span className="block break-all font-mono text-[11px]">{paths.join(', ')}</span>
            </>
          ) : exact}
          delay={200}
        >
          <time dateTime={change.timestamp} tabIndex={0} className="cursor-help text-xs tabular-nums text-theme-text-tertiary">
            {changeTimeLabel(change, anchor)}
            <span className="sr-only">{summary && paths.length > 0 ? `, ${exact}, fields: ${paths.join(', ')}` : `, ${exact}`}</span>
          </time>
        </Tooltip>
        {timelineLink}
      </div>
    </li>
  );
}

function ChangeList(props: {
  issue: Issue;
  changes: IssueRecentChange[];
  anchor: number | null;
  heading?: string;
  group: 'before' | 'after' | 'all';
  resourceHref?: (ref: IssueResourceRef) => string;
  onResourceClick?: (ref: IssueResourceRef) => void;
} & IssueTimelineLinks) {
  const { changes, heading, group, ...row } = props;
  if (changes.length === 0) return null;
  return (
    <div className="flex flex-col gap-1" data-change-group={group}>
      {heading ? <p className="text-xs font-medium text-theme-text-tertiary">{heading}</p> : null}
      <ul className="flex flex-col gap-2">
        {changes.map((change, index) => (
          <ChangeRow key={`${change.kind}/${change.namespace ?? ''}/${change.name}@${change.timestamp}#${index}`} change={change} {...row} />
        ))}
      </ul>
    </div>
  );
}

/** "Recent changes" in an expanded issue: what changed on the subject and the
 *  sources it depends on around when the issue was first seen failing. States only
 *  what was recorded; it never says a change caused the issue. */
export function IssueRecentChanges({
  issue,
  expansion,
  resourceHref,
  onResourceClick,
  timelineEventHref,
  onTimelineEventClick,
  canOpenTimelineEvent,
}: {
  issue: Issue;
  expansion: IssueExpansion;
  resourceHref?: (ref: IssueResourceRef) => string;
  onResourceClick?: (ref: IssueResourceRef) => void;
} & IssueTimelineLinks) {
  const state = expansion.recentChanges;
  const retry = expansion.onRetry ? (
    <button type="button" className={RETRY_CLASS} onClick={expansion.onRetry} disabled={expansion.retrying}>
      Retry
    </button>
  ) : null;

  let body;
  if (state.status === 'unsupported') {
    body = <div data-recent-changes="unsupported"><RadarUpgradeNote requirement={state.upgrade} /></div>;
  } else if (state.status === 'loading') {
    body = <p className={NOTE_CLASS} data-recent-changes="loading">Checking for recent changes…</p>;
  } else if (state.status === 'failed') {
    body = <p className={NOTE_CLASS} data-recent-changes="failed">{"Couldn't read recent changes."}{retry}</p>;
  } else if (state.status === 'not_found') {
    body = <p className={NOTE_CLASS} data-recent-changes="not-found">This issue changed since the list loaded.</p>;
  } else if (state.status === 'unavailable') {
    body = state.reason === 'read_error' ? (
      <p className={NOTE_CLASS} data-recent-changes="limit" data-reason="read_error">{"Couldn't read recent changes."}{retry}</p>
    ) : (
      <p className={NOTE_CLASS} data-recent-changes="limit" data-reason="user_forbidden">
        {isNativeHelm(issue)
          ? "You don't have access to this Helm release's history, so Radar can't confirm nothing changed."
          : unknownLine('user_forbidden', null)}
      </p>
    );
  } else {
    if (!recentChangesSectionShown(issue, expansion)) return null;
    const groups = groupRecentChanges(issue);
    const anchor = groups.all ? null : recentChangesAnchor(issue);
    const { lines, readError } = recentChangesLimits(issue);
    const listProps = { issue, anchor, resourceHref, onResourceClick, timelineEventHref, onTimelineEventClick, canOpenTimelineEvent };
    const windowFrom = clockTime(issue.correlation?.window_from);
    const fetchedAt = expansion.refreshFailed ? clockTime(new Date(expansion.refreshFailed.fetchedAt).toISOString()) : null;
    const showNone = noChangesShown(issue);
    body = (
      <div className="flex flex-col gap-2.5">
        {expansion.refreshFailed ? (
          <p className={NOTE_CLASS} data-recent-changes="refresh-failed">
            {fetchedAt ? `Couldn't refresh. Showing changes as of ${fetchedAt}.` : "Couldn't refresh. Showing the last changes Radar returned."}
            {retry}
          </p>
        ) : null}
        {groups.all ? (
          <ChangeList {...listProps} group="all" changes={groups.all} />
        ) : (
          <>
            <ChangeList {...listProps} group="before" changes={groups.before ?? []} heading="Before this was first seen failing" />
            <ChangeList {...listProps} group="after" changes={groups.after ?? []} heading="After this was first seen failing" />
          </>
        )}
        {showNone && issue.no_recent_changes ? (
          <p className={NOTE_CLASS} data-recent-changes="none">
            {windowFrom
              ? `No tracked changes to ${noChangesScope(issue)} since ${windowFrom}.`
              : `No tracked changes to ${noChangesScope(issue)} in the last ${span(issue.no_recent_changes.window_seconds * 1000)}.`}
            {policyOnlyAdmission(issue) ? ` Radar doesn't track changes to the ${issue.kind} itself.` : null}
          </p>
        ) : null}
        {issue.correlated_changes_more_not_shown ? <p className={NOTE_CLASS} data-recent-changes="more-not-shown">{"More changes from this time aren't shown."}</p> : null}
        {issue.correlated_changes_incomplete ? <p className={NOTE_CLASS} data-recent-changes="incomplete">{"Radar couldn't read all the changes from this time, so some may be missing."}</p> : null}
        {readError ? <p className={NOTE_CLASS} data-recent-changes="limit" data-reason="read_error">{"Couldn't read recent changes."}{retry}</p> : null}
        {lines.map((line) => (
          <p key={line.reason} className={NOTE_CLASS} data-recent-changes="limit" data-reason={line.reason}>{line.text}</p>
        ))}
      </div>
    );
  }

  return (
    <CardSection icon={History} label="Recent changes">
      {body}
    </CardSection>
  );
}
