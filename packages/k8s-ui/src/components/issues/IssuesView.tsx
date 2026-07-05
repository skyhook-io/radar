import { useEffect, useMemo, useState, type ComponentType, type ReactNode } from 'react';
import { AlertOctagon, AlertTriangle, ArrowRight, ChevronRight, CircleCheck, Clock, ExternalLink, Layers, Terminal, Workflow } from 'lucide-react';
import { CardBody, CardSection, ClusterName, EmptyState, KIND_CHIP_CLASS, TerminalBlock } from '../ui';
import { formatCompactAge, formatRelativeAgeTime } from '../../utils/format';
import { diagnosticRoleLabel, diagnosticFactLabel, confidenceTitle, incidentParentLabel } from './diagnostic';
import { issueTiming } from './issue-timing';
import {
  ISSUE_SEVERITY_BADGE_CLASS,
  ISSUE_SEVERITY_HEADER_BAND_CLASS,
  ISSUE_SEVERITY_LABEL,
  ISSUE_SEVERITY_RAIL_CLASS,
  ISSUE_SEVERITY_SOLID_CLASS,
  ISSUE_SEVERITY_TEXT_CLASS,
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
  type IssueSeverity,
} from './types';

// Leading severity glyph — the at-a-glance severity cue on every row: critical =
// octagon (stop), warning = triangle.
const ISSUE_SEVERITY_ICON: Record<IssueSeverity, ComponentType<{ className?: string }>> = {
  critical: AlertOctagon,
  warning: AlertTriangle,
};

// An out-of-contract severity the backend might emit is coerced to this tier
// once, up front (normalizeIssueSeverity), so the icon AND every color map
// (text/rail/band/pill) resolve together — a raw miss would crash the icon and
// silently drop the tint everywhere else.
const ISSUE_SEVERITY_FALLBACK: IssueSeverity = 'warning';
// Object.hasOwn (not `in`) so inherited keys like "toString" don't slip past.
const normalizeIssueSeverity = (s: IssueSeverity): IssueSeverity =>
  Object.hasOwn(ISSUE_SEVERITY_ICON, s) ? s : ISSUE_SEVERITY_FALLBACK;

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

export interface IssueRowSlotContext {
  issue: Issue;
  open: boolean;
}

export interface IssueRowProps {
  issue: Issue;
  clusterLabel?: (issue: Issue) => string | undefined;
  open: boolean;
  onToggle: () => void;
  resourceHref?: (ref: IssueResourceRef) => string;
  onResourceClick?: (ref: IssueResourceRef) => void;
  as?: 'li' | 'div';
  className?: string;
  dimmed?: boolean;
  renderBadges?: (ctx: IssueRowSlotContext) => ReactNode;
  renderMeta?: (ctx: IssueRowSlotContext) => ReactNode;
  renderActions?: (ctx: IssueRowSlotContext) => ReactNode;
  ResourceLinkIcon?: ComponentType<{ className?: string }>;
}

export function IssueRow({
  issue,
  clusterLabel,
  open,
  onToggle,
  resourceHref,
  onResourceClick,
  as = 'li',
  className,
  dimmed,
  renderBadges,
  renderMeta,
  renderActions,
  ResourceLinkIcon = ExternalLink,
}: IssueRowProps) {
  const cluster = clusterLabel?.(issue);
  const affected = affectedSummary(issue.affected);
  const { headline } = issueMessageParts(issue);
  const [renderDetails, setRenderDetails] = useState(open);
  const Container = as;
  const severity = normalizeIssueSeverity(issue.severity);
  const SeverityIcon = ISSUE_SEVERITY_ICON[severity];
  const slotCtx = { issue, open };
  const timing = issueTiming(issue);

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
    <Container
      className={[
        'overflow-hidden rounded-xl border bg-theme-surface transition-[border-color,box-shadow] duration-200',
        // The open card lifts via elevation — heavier shadow + a bright
        // emphasis edge that clearly separates it from sibling cards. Severity
        // stays rationed to the band + pill; separation is depth, not more color.
        // ring-1 widens the edge to 2px without the layout shift a border-2
        // swap would cause on expand.
        open ? 'border-[var(--border-emphasis)] ring-1 ring-[var(--border-emphasis)] shadow-theme-md' : 'border-theme-border shadow-theme-sm',
        dimmed ? 'opacity-60' : '',
        className ?? '',
      ].filter(Boolean).join(' ')}
      style={{ contentVisibility: 'auto', containIntrinsicSize: 'auto 72px' }}
    >
      {/* The whole header is the single toggle target. The leading severity
          icon is the at-a-glance cue; a trailing chevron shows open/closed.
          Deep-links live in the expanded body (a link nested in a button would
          be invalid). Collapsed: neutral row + rail. Expanded: severity-tinted
          band + solid pill — the tint is a focus signal, not per-row alarm. */}
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
        className={`group flex cursor-pointer items-center gap-3 border-l-[3px] py-3 pl-3 pr-4 transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-radar-accent)]/40 ${open ? ISSUE_SEVERITY_HEADER_BAND_CLASS[severity] : ISSUE_SEVERITY_RAIL_CLASS[severity]}`}
      >
        <SeverityIcon className={`h-[18px] w-[18px] shrink-0 ${ISSUE_SEVERITY_TEXT_CLASS[severity]}`} aria-hidden />

        <div className="flex min-w-0 flex-1 flex-col gap-1">
          <div className="flex min-w-0 items-baseline gap-2">
            <span className="shrink-0 text-sm font-medium text-theme-text-primary">{categoryLabel(issue.category)}</span>
            <span className={`shrink-0 self-center ${groupBadgeClass(issue.category_group)}`}>{groupLabel(issue.category_group)}</span>
            {renderBadges?.(slotCtx)}
            {/* The detector reason rides the title row while COLLAPSED so the
                key triage signal shows without expanding. When open, the full
                cause lives in the WHAT'S WRONG section below, so it fades out
                here (stays mounted — it's the flex-1 filler, so unmounting
                wouldn't reflow anything, but fading avoids the pop). */}
            {issue.reason ? (
              <span
                aria-hidden={open || undefined}
                className={`min-w-0 flex-1 truncate text-xs text-theme-text-tertiary transition-opacity duration-200 ${open ? 'opacity-0' : 'opacity-100'}`}
              >
                <span className="font-medium text-theme-text-secondary">{issue.reason}</span>
                {headline ? <span> — {headline}</span> : null}
              </span>
            ) : null}
          </div>
          <div className="flex min-w-0 items-center gap-1.5 text-xs text-theme-text-tertiary">
            <span className={KIND_CHIP_CLASS}>{issue.kind}</span>
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
            {issue.incident_parent ? (
              <>
                <span aria-hidden>·</span>
                {/* Non-interactive signal (the header is the toggle — a nested
                    button would be invalid); the clickable link lives in the body. */}
                <span className="min-w-0 truncate text-theme-text-tertiary" title={confidenceTitle(issue.incident_parent.confidence ?? '')}>
                  ↳ {incidentParentLabel(issue.incident_parent.fact_type, issue.incident_parent.confidence)}{' '}
                  <span className="font-medium text-theme-text-secondary">{issue.incident_parent.ref.kind} / {issue.incident_parent.ref.name}</span>
                </span>
              </>
            ) : null}
            {renderMeta?.(slotCtx)}
          </div>
        </div>

        {/* Right cluster — severity pill THEN age, vertically centered as a group. */}
        <div className="flex shrink-0 items-center gap-3">
          <span className={`badge-sm shrink-0 px-2.5 py-0.5 text-xs font-semibold ${open ? ISSUE_SEVERITY_SOLID_CLASS[severity] : ISSUE_SEVERITY_BADGE_CLASS[severity]}`}>
            {ISSUE_SEVERITY_LABEL[severity]}
          </span>
          {/* Age chip: keep recency visible; the deployment-start signal rides as a secondary tag. */}
          {issue.first_seen ? (
            <time
              dateTime={issue.first_seen}
              title={ageTitle(issue)}
              className="flex shrink-0 items-center gap-1 text-xs tabular-nums text-theme-text-tertiary"
            >
              <Clock className="h-3 w-3" aria-hidden />
              {formatCompactAge(issue.first_seen)}
              {timing ? (
                <span className="badge-sm ml-1 text-[10px] text-theme-text-secondary" title={timing.tooltip}>{timing.chip}</span>
              ) : null}
            </time>
          ) : null}
          {renderActions?.(slotCtx)}
          <ChevronRight className={`h-4 w-4 shrink-0 text-theme-text-tertiary transition-transform duration-200 ${open ? 'rotate-90' : ''}`} />
        </div>
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
            {/* Body sits on the card surface (not a recessed grey panel) so its
                text keeps enough contrast. */}
            <div className="border-t border-theme-border bg-theme-surface py-4 pl-6 pr-4">
              <div className="flex flex-col divide-y divide-theme-border/70 [&>*]:py-4 [&>*:first-child]:pt-0 [&>*:last-child]:pb-0">
                <Diagnosis issue={issue} />
                {issue.incident_parent ? (
                  <section className="flex flex-col gap-1">
                    <h4 className="text-[11px] font-semibold uppercase tracking-wide text-theme-text-tertiary">
                      {incidentParentLabel(issue.incident_parent.fact_type, issue.incident_parent.confidence)}
                      {issue.incident_parent.confidence ? (
                        <span className="ml-2 badge-sm text-[10px] font-normal text-theme-text-tertiary" title={confidenceTitle(issue.incident_parent.confidence)}>
                          {issue.incident_parent.confidence} confidence
                        </span>
                      ) : null}
                    </h4>
                    <ul className="flex flex-col gap-px">
                      <ResourceLine refForLink={memberRef(issue, issue.incident_parent.ref)} resourceHref={resourceHref} onResourceClick={onResourceClick} ResourceLinkIcon={ResourceLinkIcon} />
                    </ul>
                  </section>
                ) : null}
                <DiagnosticContext issue={issue} resourceHref={resourceHref} onResourceClick={onResourceClick} ResourceLinkIcon={ResourceLinkIcon} />
                <AffectedResources issue={issue} resourceHref={resourceHref} onResourceClick={onResourceClick} ResourceLinkIcon={ResourceLinkIcon} />
              </div>
            </div>
          </div>
        </div>
      ) : null}
    </Container>
  );
}

// Diagnosis: WHAT'S WRONG (amber) → NEXT STEP (emerald) → RAW ERROR (monochrome
// terminal block). Status/timing facts share one muted meta line under the
// diagnosis so the body reads as three beats rather than a stack of one-liners.
function Diagnosis({ issue }: { issue: Issue }) {
  const crash =
    issue.restart_count || issue.last_terminated_reason
      ? [issue.restart_count ? `${issue.restart_count} restart${issue.restart_count === 1 ? '' : 's'}` : null, issue.last_terminated_reason ? `last exit: ${issue.last_terminated_reason}` : null]
          .filter(Boolean)
          .join(' · ')
      : null;
  const { headline, detail } = issueMessageParts(issue);
  // Raw detector text → terminal block. Two independent sources: the explicit
  // raw_message (or, for a derived cause, the original message it came from) and
  // the precise kubelet/containerd `detail` a short image-pull headline replaced
  // — both surface when distinct, so setting raw_message never hides the CRI
  // detail. Each is kept only when it adds something the prose above doesn't
  // already show, compared against what's actually rendered (the cause when
  // present, else headline+detail) rather than a string that may never appear.
  const visibleMessage = [headline, detail].filter(Boolean).join(' ');
  const shownText = issue.cause ?? visibleMessage;
  const timing = issueTiming(issue);
  const rawError = [
    ...new Set(
      [issue.raw_message ?? (issue.cause ? issue.message : undefined), detail].filter(
        (s): s is string => !!s && s !== shownText,
      ),
    ),
  ].join('\n');

  // One muted line — stuck/retry · crash · timing · change. A retrying-but-not-
  // yet-stuck operation leads with "Retrying"; once it's declared stuck, "Stuck"
  // takes over.
  const meta: string[] = [];
  if (issue.stuck) meta.push('Stuck');
  else if (issue.operation_retry_count) meta.push('Retrying');
  if (issue.operation_retry_count) meta.push(`retried ${issue.operation_retry_count}×`);
  if (crash) meta.push(crash);
  if (timing) {
    meta.push(timing.meta);
  } else if (issue.first_seen) {
    meta.push(`started ${formatRelativeAgeTime(issue.first_seen)}`);
  }
  if (issue.first_seen) {
    if (issue.last_seen && timing?.kind !== 'creation') meta.push(`last seen ${formatRelativeAgeTime(issue.last_seen)}`);
  }
  if (issue.change_context) meta.push(changeContextText(issue.change_context));

  const hasNextStep = !!issue.action || (issue.remediation_kind === 'create-namespace' && !!issue.remediation_target);

  return (
    <div className="flex flex-col divide-y divide-theme-border/70 [&>*]:py-4 [&>*:first-child]:pt-0 [&>*:last-child]:pb-0">
      {/* When a parsed cause replaces the detector text, the terse reason code
          (CrashLoopBackOff, FailedMount…) rides the eyebrow — it's the greppable
          identifier operators anchor on, and the collapsed header drops it while
          open. The non-cause branch already leads with it in the prose. */}
      <CardSection
        icon={AlertTriangle}
        label="What's wrong"
        tone="warn"
        labelExtra={issue.cause && issue.reason ? `· ${issue.reason}` : undefined}
      >
        {issue.cause ? (
          <p className="text-sm leading-relaxed text-theme-text-primary">{issue.cause}</p>
        ) : (
          <p className="text-sm leading-relaxed text-theme-text-primary">
            <span className="font-medium">{issue.reason}</span>
            {headline ? <span className="text-theme-text-secondary"> — {headline}</span> : null}
          </p>
        )}
        {meta.length > 0 ? <p className="text-xs leading-relaxed text-theme-text-tertiary tabular-nums">{meta.join(' · ')}</p> : null}
      </CardSection>

      {hasNextStep ? (
        <CardSection icon={ArrowRight} label="Next step" tone="fix">
          {issue.action ? <CardBody>{issue.action}</CardBody> : null}
          {issue.remediation_kind === 'create-namespace' && issue.remediation_target ? (
            <p className="text-xs text-theme-text-tertiary">
              Suggested fix: create namespace <code className="inline-code">{issue.remediation_target}</code> — apply it from the GitOps detail page.
            </p>
          ) : null}
        </CardSection>
      ) : null}

      {rawError ? (
        <CardSection icon={Terminal} label="Raw error">
          <TerminalBlock>{rawError}</TerminalBlock>
        </CardSection>
      ) : null}
    </div>
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
  ResourceLinkIcon,
}: {
  issue: Issue;
  resourceHref?: (ref: IssueResourceRef) => string;
  onResourceClick?: (ref: IssueResourceRef) => void;
  ResourceLinkIcon: ComponentType<{ className?: string }>;
}) {
  const ctx = issue.diagnostic_context;
  const facts = ctx?.facts?.filter((fact) => fact.message || fact.refs?.length || fact.related_issues?.length) ?? [];
  if (!ctx || facts.length === 0) return null;

  return (
    <CardSection
      icon={Workflow}
      label="Context"
      labelExtra={ctx.role ? diagnosticRoleLabel(ctx.role) : undefined}
    >
      <ul className="flex flex-col gap-2">
        {facts.map((fact, idx) => (
          <li key={`${fact.type}-${idx}`} className="flex flex-col gap-1.5 rounded-md border border-theme-border/70 px-2.5 py-2">
            <div className="flex min-w-0 items-baseline gap-2">
              <span className="shrink-0 text-xs font-medium text-theme-text-secondary">{diagnosticFactLabel(fact.type)}</span>
              {fact.confidence ? (
                <span
                  className="shrink-0 badge-sm text-[10px] text-theme-text-tertiary"
                  title={confidenceTitle(fact.confidence)}
                >
                  {fact.confidence} confidence
                </span>
              ) : null}
              {fact.message ? <span className="min-w-0 break-words text-xs leading-relaxed text-theme-text-tertiary">{fact.message}</span> : null}
            </div>
            {fact.related_issues?.length ? (
              <ul className="flex flex-col gap-px">
                {fact.related_issues.map((related, relIdx) => (
                  <ResourceLine
                    key={`${related.ref.group ?? ''}/${related.ref.kind}/${related.ref.namespace ?? ''}/${related.ref.name}#${relIdx}`}
                    label="Related"
                    refForLink={memberRef(issue, related.ref)}
                    count={related.count}
                    resourceHref={resourceHref}
                    onResourceClick={onResourceClick}
                    ResourceLinkIcon={ResourceLinkIcon}
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
                    ResourceLinkIcon={ResourceLinkIcon}
                  />
                ))}
              </ul>
            ) : null}
          </li>
        ))}
      </ul>
    </CardSection>
  );
}

// Native-tooltip detail for the collapsed-row age chip: absolute first-seen + last-seen
// freshness, the two facts the compact "2h" hides.
function ageTitle(issue: Issue): string {
  const parts: string[] = [];
  const timing = issueTiming(issue);
  if (timing) parts.push(timing.tooltip);
  if (issue.first_seen) parts.push(`First seen ${new Date(issue.first_seen).toLocaleString()}`);
  if (issue.last_seen) parts.push(`Last seen ${formatRelativeAgeTime(issue.last_seen)}`);
  return parts.join('\n');
}

function AffectedResources({
  issue,
  resourceHref,
  onResourceClick,
  ResourceLinkIcon,
}: {
  issue: Issue;
  resourceHref?: (ref: IssueResourceRef) => string;
  onResourceClick?: (ref: IssueResourceRef) => void;
  ResourceLinkIcon: ComponentType<{ className?: string }>;
}) {
  const members = issue.members ?? [];
  // count is the backend fan-out size (members, subject excluded — see
  // grouping.go); fall back to the inline member count, not +1.
  const total = issue.count ?? members.length;
  return (
    <section className="flex flex-col gap-3">
      {/* The subject (the grouped thing — e.g. the Deployment) is always the
          first deep-link; members (the folded pods) follow. ResourceLine emits
          an <li>, so it needs a list parent of its own. */}
      <ul className="flex flex-col gap-px">
        <ResourceLine label="Subject" compact refForLink={subjectRef(issue)} resourceHref={resourceHref} onResourceClick={onResourceClick} ResourceLinkIcon={ResourceLinkIcon} />
      </ul>
      {members.length > 0 && (
        <CardSection icon={Layers} label="Affected resources" labelExtra={`· ${total}`}>
          <ul className="flex flex-col gap-px">
            {members.map((m, i) => (
              <ResourceLine
                key={`${m.group}/${m.kind}/${m.namespace}/${m.name}#${i}`}
                refForLink={memberRef(issue, m)}
                resourceHref={resourceHref}
                onResourceClick={onResourceClick}
                ResourceLinkIcon={ResourceLinkIcon}
              />
            ))}
          </ul>
          {issue.members_truncated && (
            <p className="text-xs text-theme-text-tertiary">
              Showing {members.length} of {total} — open the subject to see the rest.
            </p>
          )}
        </CardSection>
      )}
    </section>
  );
}

function ResourceLine({
  label,
  refForLink,
  count,
  resourceHref,
  onResourceClick,
  ResourceLinkIcon,
  compact,
}: {
  label?: string;
  refForLink: IssueResourceRef;
  count?: number;
  resourceHref?: (ref: IssueResourceRef) => string;
  onResourceClick?: (ref: IssueResourceRef) => void;
  ResourceLinkIcon: ComponentType<{ className?: string }>;
  // Footer variant (the Subject row): renders the kind as a grey mono chip and
  // a slightly bolder name, distinguishing the subject from the affected-members
  // list below it.
  compact?: boolean;
}) {
  const r = refForLink;
  const linkable = !!(onResourceClick || resourceHref);
  const body = (
    <>
      {label ? <span className="shrink-0 text-[11px] font-semibold uppercase tracking-[0.06em] text-theme-text-tertiary">{label}</span> : null}
      {/* Footer (compact): the kind is a grey mono chip; member rows keep plain mono. */}
      {compact ? (
        <span className={KIND_CHIP_CLASS}>{r.kind}</span>
      ) : (
        <span className="shrink-0 font-mono text-[11px] uppercase tracking-wide text-theme-text-tertiary">{r.kind}</span>
      )}
      <span className={`min-w-0 truncate text-sm ${linkable ? `${compact ? 'font-semibold' : 'font-medium'} text-[var(--color-radar-accent)]` : 'font-medium text-theme-text-primary'}`}>
        {r.namespace ? `${r.namespace} / ` : ''}
        {r.name}
      </span>
      {count && count > 1 ? (
        <span className="shrink-0 text-[10px] text-theme-text-tertiary tabular-nums" title={`${count} affected resources grouped under this issue`}>{count} affected</span>
      ) : null}
      {linkable && <ResourceLinkIcon className="h-3 w-3 shrink-0 text-theme-text-tertiary opacity-0 transition-opacity group-hover/r:opacity-100" />}
    </>
  );
  // compact = Subject row; default = the affected-members list. Both get the
  // padded, rounded hover hit-box so the Subject reads as clickable like the
  // members below it. items-baseline so the label + kind chip sit on the same
  // baseline as the larger resource name.
  const cls = compact
    ? 'group/r flex w-full items-baseline gap-2 rounded-md px-2 py-1 text-left transition-colors hover:bg-theme-hover/60'
    : 'group/r flex w-full items-baseline gap-2 rounded-md px-2 py-1 text-left text-sm transition-colors hover:bg-theme-hover/60';
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
        <span className={compact ? 'flex items-baseline gap-2 rounded-md px-2 py-1' : 'flex items-baseline gap-2 rounded-md px-2 py-1 text-sm'}>{body}</span>
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
