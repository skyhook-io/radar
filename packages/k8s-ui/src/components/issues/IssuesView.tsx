import { useEffect, useMemo, useState, type ReactNode } from 'react';
import { ChevronRight, CircleCheck, Clock, ExternalLink } from 'lucide-react';
import { ClusterName, EmptyState } from '../ui';
import { formatCompactAge, formatRelativeAgeTime } from '../../utils/format';
import {
  ISSUE_SEVERITY_BADGE_CLASS,
  ISSUE_SEVERITY_LABEL,
  ISSUE_SEVERITY_RAIL_CLASS,
  categoryLabel,
  groupBadgeClass,
  groupLabel,
} from './severity';
import {
  compareIssues,
  issueMessageParts,
  memberRef,
  subjectRef,
  type Issue,
  type IssueAffected,
  type IssueResourceRef,
} from './types';

export interface IssuesViewProps {
  /** Grouped live issues — one row per subject+category. Typically flattened
   *  across the fleet by the host (the hub) or a single cluster (OSS). */
  issues: Issue[];
  /** True when at least one source returned issue data — distinguishes "clean"
   *  from "nothing connected / everything errored". */
  anyData: boolean;
  /** Resolve a deep-link href for a resource (host-specific routing). Omit to
   *  render non-link text. */
  resourceHref?: (ref: IssueResourceRef) => string;
  /** In-app resource navigation. When set, resource lines call this (no reload)
   *  instead of following resourceHref — OSS opens its own drawer this way.
   *  Takes precedence over resourceHref. */
  onResourceClick?: (ref: IssueResourceRef) => void;
  /** Display label for an issue's source cluster. Omit (or return falsy) to
   *  hide the cluster line — e.g. single-cluster OSS. */
  clusterLabel?: (issue: Issue) => string | undefined;
  /** Empty-state CTA shown when there's no data. */
  emptyAction?: ReactNode;
}

// The queue list. Filtering/faceting is the host page's job (FleetPageShell on
// the hub, a thin wrapper in OSS) — this renders the rows + the healthy /
// no-data terminal states only.
export function IssuesView({ issues, anyData, resourceHref, onResourceClick, clusterLabel, emptyAction }: IssuesViewProps) {
  // Single-open accordion: opening a row collapses the previous one, so the
  // queue stays scannable and you never lose your place to a wall of expansions.
  const [openId, setOpenId] = useState<string | null>(null);

  // Stable order keyed on severity → observed age → identity (see compareIssues), so
  // the queue doesn't reshuffle under the host's auto-refresh.
  const sorted = useMemo(() => [...issues].sort(compareIssues), [issues]);

  if (sorted.length === 0) {
    return anyData ? (
      <EmptyState
        tone="healthy"
        variant="card"
        icon={CircleCheck}
        headline="Nothing broken right now"
        body="No active issues across the selected scope."
      />
    ) : (
      <EmptyState headline="No issue data yet" body="Connect a cluster to populate the issue queue." action={emptyAction} />
    );
  }

  return (
    <ol className="flex flex-col gap-1.5">
      {sorted.map((issue) => {
        // Stable identity for the React key + open-accordion state, so a row
        // survives auto-refresh in place. cluster_id scopes the id across the
        // fleet (the hub renders issues from many clusters in one list).
        const rowKey = `${issue.cluster_id ?? ''}:${issue.id}`;
        return (
          <IssueRow
            key={rowKey}
            issue={issue}
            clusterLabel={clusterLabel}
            open={openId === rowKey}
            onToggle={() => setOpenId((cur) => (cur === rowKey ? null : rowKey))}
            resourceHref={resourceHref}
            onResourceClick={onResourceClick}
          />
        );
      })}
    </ol>
  );
}

function IssueRow({
  issue,
  clusterLabel,
  open,
  onToggle,
  resourceHref,
  onResourceClick,
}: {
  issue: Issue;
  clusterLabel?: (issue: Issue) => string | undefined;
  open: boolean;
  onToggle: () => void;
  resourceHref?: (ref: IssueResourceRef) => string;
  onResourceClick?: (ref: IssueResourceRef) => void;
}) {
  const cluster = clusterLabel?.(issue);
  const affected = affectedSummary(issue.affected);
  const { headline } = issueMessageParts(issue);
  const [renderDetails, setRenderDetails] = useState(open);

  useEffect(() => {
    if (open) {
      setRenderDetails(true);
      return;
    }
    if (!renderDetails) return;

    const timeout = window.setTimeout(() => setRenderDetails(false), 200);
    return () => window.clearTimeout(timeout);
  }, [open, renderDetails]);

  return (
    <li
      className="overflow-hidden rounded-xl border border-theme-border bg-theme-surface shadow-theme-sm"
      style={{ contentVisibility: 'auto', containIntrinsicSize: 'auto 72px' }}
    >
      {/* The whole header is the single toggle target — chevron is just the
          open/closed indicator, not a separate action. Deep-links live in the
          expanded body (a link nested in a button would be invalid). */}
      <div
        role="button"
        tabIndex={0}
        aria-expanded={open}
        onClick={onToggle}
        onKeyDown={(e) => {
          if (e.target !== e.currentTarget) return;
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault();
            onToggle();
          }
        }}
        className={`group flex cursor-pointer items-center gap-3 border-l-2 py-3 pl-3 pr-4 transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-radar-accent)]/40 ${ISSUE_SEVERITY_RAIL_CLASS[issue.severity]}`}
      >
        <ChevronRight className={`h-4 w-4 shrink-0 text-theme-text-tertiary transition-transform duration-200 ${open ? 'rotate-90' : ''}`} />

        <div className="flex min-w-0 flex-1 flex-col gap-1">
          <div className="flex min-w-0 items-baseline gap-2">
            <span className="shrink-0 text-sm font-medium text-theme-text-primary">{categoryLabel(issue.category)}</span>
            <span className={`badge-sm shrink-0 self-center text-[10px] ${groupBadgeClass(issue.category_group)}`}>{groupLabel(issue.category_group)}</span>
            {/* The detector reason/message rides the title row so the most
                useful triage signal is visible without expanding — it fills
                the otherwise-empty band between the title and the severity
                badge. Full text (plus crash context) stays in the body. */}
            {issue.reason ? (
              <span className="min-w-0 flex-1 truncate text-xs text-theme-text-tertiary">
                <span className="font-medium text-theme-text-secondary">{issue.reason}</span>
                {headline ? <span> — {headline}</span> : null}
              </span>
            ) : null}
          </div>
          <div className="flex min-w-0 items-center gap-1.5 text-xs text-theme-text-tertiary">
            <span className="shrink-0 font-mono uppercase tracking-wide">{issue.kind}</span>
            <span className="min-w-0 truncate font-medium text-theme-text-secondary">
              {issue.namespace ? `${issue.namespace} / ` : ''}
              {issue.name}
            </span>
            {cluster ? (
              <>
                <span aria-hidden>·</span>
                <span className="max-w-[160px] shrink-0 truncate">
                  <ClusterName name={cluster} />
                </span>
              </>
            ) : null}
            {affected ? (
              <>
                <span aria-hidden>·</span>
                <span className="shrink-0 tabular-nums">{affected}</span>
              </>
            ) : null}
          </div>
        </div>

        {/* Age chip: chronic-vs-acute signal. When issue_timing is
            started_at_resource_creation, swap the raw age for "since deploy" —
            the raw age is misleading (it reads as urgency, but this issue has
            been present since the resource was first created). */}
        {issue.first_seen ? (
          <time
            dateTime={issue.first_seen}
            title={ageTitle(issue)}
            className="flex shrink-0 items-center gap-1 text-xs tabular-nums text-theme-text-tertiary"
          >
            <Clock className="h-3 w-3" aria-hidden />
            {issue.issue_timing === 'started_at_resource_creation'
              ? 'since deploy'
              : formatCompactAge(issue.first_seen)}
          </time>
        ) : null}

        <span className={`badge-sm shrink-0 text-[10px] font-semibold ${ISSUE_SEVERITY_BADGE_CLASS[issue.severity]}`}>
          {ISSUE_SEVERITY_LABEL[issue.severity]}
        </span>
      </div>

      {renderDetails ? (
        <div
          className={`issue-details-motion ${open ? 'issue-details-motion-open' : ''}`}
          onTransitionEnd={(event) => {
            if (event.target !== event.currentTarget) return;
            if (event.propertyName !== 'grid-template-rows') return;
            if (!open) setRenderDetails(false);
          }}
        >
          <div className="overflow-hidden">
            <div className="border-t border-theme-border bg-theme-base/40 px-4 py-4 pl-11">
              <div className="flex flex-col gap-4">
                <Diagnosis issue={issue} />
                <DiagnosticContext issue={issue} resourceHref={resourceHref} onResourceClick={onResourceClick} />
                <div className="border-t border-theme-border/70 pt-3">
                  <AffectedResources issue={issue} resourceHref={resourceHref} onResourceClick={onResourceClick} />
                </div>
              </div>
            </div>
          </div>
        </div>
      ) : null}
    </li>
  );
}

// What's-wrong block: the specific detector reason + message, plus pod crash
// context when present (the "chronic vs acute" signal).
function Diagnosis({ issue }: { issue: Issue }) {
  const crash =
    issue.restart_count || issue.last_terminated_reason
      ? [issue.restart_count ? `${issue.restart_count} restart${issue.restart_count === 1 ? '' : 's'}` : null, issue.last_terminated_reason ? `last exit: ${issue.last_terminated_reason}` : null]
          .filter(Boolean)
          .join(' · ')
      : null;
  const { headline, detail } = issueMessageParts(issue);
  return (
    <section className="flex flex-col gap-1">
      <h4 className="text-[11px] font-semibold uppercase tracking-wide text-theme-text-tertiary">What's wrong</h4>
      <p className="text-sm leading-relaxed text-theme-text-primary">
        <span className="font-medium">{issue.reason}</span>
        {headline ? <span className="text-theme-text-secondary"> — {headline}</span> : null}
      </p>
      {/* Raw source string kept as secondary detail only when we showed a
          normalized headline above (e.g. the verbose containerd image-pull
          error) — so the precise message is never lost, just de-emphasized. */}
      {detail ? <p className="break-words font-mono text-xs leading-relaxed text-theme-text-tertiary">{detail}</p> : null}
      {crash ? <p className="text-xs text-theme-text-tertiary tabular-nums">{crash}</p> : null}
      {issue.change_context ? (
        <p className="text-xs text-theme-text-tertiary">
          {changeContextText(issue.change_context)}
        </p>
      ) : null}
      {issue.first_seen ? (
        <p className="text-xs text-theme-text-tertiary tabular-nums">
          {issue.issue_timing === 'started_at_resource_creation'
            ? 'Present since deployment'
            : issue.issue_timing === 'started_after_resource_was_healthy'
              ? `Started ${formatRelativeAgeTime(issue.first_seen)} · was previously healthy`
              : `Started ${formatRelativeAgeTime(issue.first_seen)}`}
          {issue.last_seen && issue.issue_timing !== 'started_at_resource_creation'
            ? ` · last seen ${formatRelativeAgeTime(issue.last_seen)}`
            : ''}
        </p>
      ) : null}
    </section>
  );
}

function changeContextText(change: NonNullable<Issue['change_context']>): string {
  const parts = [change.when ? `Changed ${change.when} ago` : 'Changed', change.what ? change.what.replace(/_/g, ' ') : null, change.evidence].filter(Boolean);
  return parts.join(' · ');
}

function DiagnosticContext({
  issue,
  resourceHref,
  onResourceClick,
}: {
  issue: Issue;
  resourceHref?: (ref: IssueResourceRef) => string;
  onResourceClick?: (ref: IssueResourceRef) => void;
}) {
  const ctx = issue.diagnostic_context;
  const facts = ctx?.facts?.filter((fact) => fact.message || fact.refs?.length || fact.related_issues?.length) ?? [];
  if (!ctx || facts.length === 0) return null;

  return (
    <section className="flex flex-col gap-2">
      <div className="flex items-center gap-2">
        <h4 className="text-[11px] font-semibold uppercase tracking-wide text-theme-text-tertiary">Context</h4>
        {ctx.role ? <span className="badge-sm text-[10px] text-theme-text-secondary">{diagnosticRoleLabel(ctx.role)}</span> : null}
      </div>
      <ul className="flex flex-col gap-2">
        {facts.map((fact, idx) => (
          <li key={`${fact.type}-${idx}`} className="flex flex-col gap-1.5 rounded-md border border-theme-border/70 px-2.5 py-2">
            <div className="flex min-w-0 items-baseline gap-2">
              <span className="shrink-0 text-xs font-medium text-theme-text-secondary">{diagnosticFactLabel(fact.type)}</span>
              {fact.message ? <span className="min-w-0 break-words text-xs leading-relaxed text-theme-text-tertiary">{fact.message}</span> : null}
            </div>
            {fact.related_issues?.length ? (
              <ul className="flex flex-col gap-px">
                {fact.related_issues.map((related, relIdx) => (
                  <ResourceLine
                    key={`${related.ref.group ?? ''}/${related.ref.kind}/${related.ref.namespace ?? ''}/${related.ref.name}#${relIdx}`}
                    label="Related"
                    refForLink={memberRef(issue, related.ref)}
                    resourceHref={resourceHref}
                    onResourceClick={onResourceClick}
                  />
                ))}
              </ul>
            ) : null}
            {fact.refs?.length ? (
              <ul className="flex flex-col gap-px">
                {fact.refs.map((ref, refIdx) => (
                  <ResourceLine
                    key={`${ref.group ?? ''}/${ref.kind}/${ref.namespace ?? ''}/${ref.name}#${refIdx}`}
                    refForLink={memberRef(issue, ref)}
                    resourceHref={resourceHref}
                    onResourceClick={onResourceClick}
                  />
                ))}
              </ul>
            ) : null}
          </li>
        ))}
      </ul>
    </section>
  );
}

function diagnosticRoleLabel(role: string): string {
  switch (role) {
    case 'candidate':
      return 'Candidate signal';
    case 'affected':
      return 'Affected signal';
    case 'rollup':
      return 'Rollup';
    default:
      return 'Context';
  }
}

function diagnosticFactLabel(type: string): string {
  switch (type) {
    case 'explicit_reference':
      return 'Explicit reference';
    case 'owner_rollup':
      return 'Owner rollup';
    case 'selected_backend_issue':
      return 'Selected backend';
    case 'service_config_mismatch':
      return 'Service config';
    case 'service_env_reference':
      return 'Service env';
    case 'probe_target_mismatch':
      return 'Probe target';
    case 'blocked_init_container':
      return 'Init container';
    case 'restart_cause':
      return 'Restart cause';
    default:
      return type.replace(/_/g, ' ');
  }
}

// Native-tooltip detail for the collapsed-row age chip: absolute first-seen + last-seen
// freshness, the two facts the compact "2h" hides.
function ageTitle(issue: Issue): string {
  const parts: string[] = [];
  if (issue.issue_timing === 'started_at_resource_creation') {
    parts.push('Present since deployment — treat as baseline');
  } else if (issue.issue_timing === 'started_after_resource_was_healthy') {
    parts.push('Resource was previously healthy');
  }
  if (issue.first_seen) parts.push(`First seen ${new Date(issue.first_seen).toLocaleString()}`);
  if (issue.last_seen) parts.push(`Last seen ${formatRelativeAgeTime(issue.last_seen)}`);
  return parts.join('\n');
}

function AffectedResources({
  issue,
  resourceHref,
  onResourceClick,
}: {
  issue: Issue;
  resourceHref?: (ref: IssueResourceRef) => string;
  onResourceClick?: (ref: IssueResourceRef) => void;
}) {
  const members = issue.members ?? [];
  // count is the backend fan-out size (members, subject excluded — see
  // grouping.go); fall back to the inline member count, not +1.
  const total = issue.count ?? members.length;
  return (
    <section className="flex flex-col gap-1.5">
      {/* The subject (the grouped thing — e.g. the Deployment) is always the
          first deep-link; members (the folded pods) follow. ResourceLine emits
          an <li>, so it needs a list parent of its own. */}
      <ul className="flex flex-col gap-px">
        <ResourceLine label="Subject" refForLink={subjectRef(issue)} resourceHref={resourceHref} onResourceClick={onResourceClick} />
      </ul>
      {members.length > 0 && (
        <>
          <h4 className="mt-1.5 text-[11px] font-semibold uppercase tracking-wide text-theme-text-tertiary">
            Affected resources <span className="tabular-nums">({total})</span>
          </h4>
          <ul className="flex flex-col gap-px">
            {members.map((m, i) => (
              <ResourceLine
                key={`${m.group}/${m.kind}/${m.namespace}/${m.name}#${i}`}
                refForLink={memberRef(issue, m)}
                resourceHref={resourceHref}
                onResourceClick={onResourceClick}
              />
            ))}
          </ul>
          {issue.members_truncated && (
            <p className="mt-0.5 text-xs text-theme-text-tertiary">
              Showing {members.length} of {total} — open the subject to see the rest.
            </p>
          )}
        </>
      )}
    </section>
  );
}

function ResourceLine({
  label,
  refForLink,
  resourceHref,
  onResourceClick,
}: {
  label?: string;
  refForLink: IssueResourceRef;
  resourceHref?: (ref: IssueResourceRef) => string;
  onResourceClick?: (ref: IssueResourceRef) => void;
}) {
  const r = refForLink;
  const linkable = !!(onResourceClick || resourceHref);
  const body = (
    <>
      {label ? <span className="shrink-0 text-[10px] font-semibold uppercase tracking-wide text-theme-text-tertiary">{label}</span> : null}
      <span className="shrink-0 font-mono text-[11px] uppercase tracking-wide text-theme-text-tertiary">{r.kind}</span>
      <span className={`min-w-0 truncate font-medium ${linkable ? 'text-[var(--color-radar-accent)]' : 'text-theme-text-primary'}`}>
        {r.namespace ? `${r.namespace} / ` : ''}
        {r.name}
      </span>
      {linkable && <ExternalLink className="h-3 w-3 shrink-0 text-theme-text-tertiary opacity-0 transition-opacity group-hover/r:opacity-100" />}
    </>
  );
  const cls = 'group/r flex w-full items-center gap-2 rounded-md px-2 py-1 text-left text-sm transition-colors hover:bg-theme-hover/60';
  return (
    <li>
      {onResourceClick ? (
        <button type="button" onClick={() => onResourceClick(r)} className={cls}>
          {body}
        </button>
      ) : resourceHref ? (
        <a href={resourceHref(r)} className={cls}>
          {body}
        </a>
      ) : (
        <span className="flex items-center gap-2 rounded-md px-2 py-1 text-sm">{body}</span>
      )}
    </li>
  );
}

// "3 pods · 1 service" from the affected rollup; null when there's no fan-out
// (single-resource issue — the subject line already says everything).
function affectedSummary(a?: IssueAffected): string | null {
  if (!a) return null;
  const parts: string[] = [];
  const add = (n: number | undefined, singular: string, plural: string) => {
    if (n && n > 0) parts.push(`${n} ${n === 1 ? singular : plural}`);
  };
  add(a.pods, 'pod', 'pods');
  add(a.workloads, 'workload', 'workloads');
  add(a.services, 'service', 'services');
  add(a.pvcs, 'PVC', 'PVCs');
  add(a.nodes, 'node', 'nodes');
  return parts.length > 0 ? parts.join(' · ') : null;
}
