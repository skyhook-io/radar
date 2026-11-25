import type { ComponentType } from 'react';
import { KIND_CHIP_CLASS } from '../ui';
import { Tooltip } from '../ui/Tooltip';
import type { IssueResourceRef } from './types';

// One deep-linkable resource in an expanded issue: a button when the host
// navigates in-app, a link when it routes by href, plain text otherwise.
export function ResourceLine({
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
      <span className={`min-w-0 truncate text-sm ${linkable ? `${compact ? 'font-semibold' : 'font-medium'} text-[var(--color-radar-accent,var(--accent))]` : 'font-medium text-theme-text-primary'}`}>
        {r.namespace ? `${r.namespace} / ` : ''}
        {r.name}
      </span>
      {count && count > 1 ? (
        <Tooltip content={`${count} affected resources grouped under this issue`} delay={200}>
          <span className="shrink-0 text-[10px] text-theme-text-tertiary tabular-nums">{count} affected</span>
        </Tooltip>
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
