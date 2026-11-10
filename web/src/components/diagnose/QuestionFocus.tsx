import { ArrowRight } from "lucide-react";

import type { DiagnosisResourceRef } from "./diagnoseEvidenceTypes";
import {
  formatInvestigationTarget,
  type InvestigationTargetIdentity,
} from "./target";

// Where a question-only investigation came to rest. Investigating one of these
// starts a run about that resource with the same question, which is where a
// fix can be confirmed and applied. The earlier verdict is not carried over:
// it was written from cluster data, and the new run's question must stay the
// engineer's own words.
export function QuestionFocus({
  focus,
  onOpenResource,
  onInvestigate,
  onChooseResource,
  disabled,
  stale = false,
}: {
  focus: InvestigationTargetIdentity[];
  onOpenResource?: (ref: DiagnosisResourceRef) => void;
  onInvestigate: (resource: InvestigationTargetIdentity) => void;
  /** Ask the same question about a resource the engineer picks. */
  onChooseResource: () => void;
  disabled?: boolean;
  /** The run is about another cluster: its resources can't be opened or
   *  investigated from this one. */
  stale?: boolean;
}) {
  if (stale) {
    return focus.length > 0 ? (
      <section aria-label="Focused on" className="card-inner px-3 py-2">
        <h3 className="text-xs font-medium text-theme-text-tertiary">
          Focused on
        </h3>
        <ul className="mt-1 space-y-1 text-sm text-theme-text-primary">
          {focus.map((resource) => (
            <li key={formatInvestigationTarget(resource)} className="truncate">
              {formatInvestigationTarget(resource)}
            </li>
          ))}
        </ul>
        <p className="mt-1.5 text-[11px] text-theme-text-tertiary">
          In the cluster this investigation ran against. This investigation
          ended when Radar switched clusters; connect to that cluster and find
          them there to investigate one.
        </p>
      </section>
    ) : null;
  }
  const choose = (
    <button
      type="button"
      disabled={disabled}
      onClick={onChooseResource}
      className="text-xs text-accent-text hover:underline disabled:opacity-50"
    >
      {focus.length > 0
        ? "Choose another resource…"
        : "Choose a resource to investigate…"}
    </button>
  );
  if (focus.length === 0) {
    return (
      <section aria-label="Focused on" className="card-inner px-3 py-2">
        <p className="text-xs text-theme-text-tertiary">
          This assessment didn’t single out one resource Radar read. A fix is
          applied from an investigation of the resource it changes.
        </p>
        <div className="mt-1">{choose}</div>
      </section>
    );
  }
  return (
    <section aria-label="Focused on" className="card-inner px-3 py-2">
      <h3 className="text-xs font-medium text-theme-text-tertiary">
        Focused on
      </h3>
      <ul className="mt-1 space-y-1">
        {focus.map((resource) => {
          const label = formatInvestigationTarget(resource);
          return (
            <li
              key={label}
              className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 text-sm"
            >
              {onOpenResource ? (
                <button
                  type="button"
                  onClick={() => onOpenResource(resource)}
                  className="min-w-0 truncate text-left text-theme-text-primary hover:underline"
                >
                  {label}
                </button>
              ) : (
                <span className="min-w-0 truncate text-theme-text-primary">
                  {label}
                </span>
              )}
              <button
                type="button"
                disabled={disabled}
                onClick={() => onInvestigate(resource)}
                className="inline-flex shrink-0 items-center gap-1 text-xs text-accent-text hover:underline disabled:opacity-50"
              >
                Investigate this resource
                <ArrowRight className="h-3 w-3" />
              </button>
            </li>
          );
        })}
      </ul>
      <div className="mt-1.5 flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
        <p className="text-[11px] text-theme-text-tertiary">
          A fix is applied from an investigation of the resource it changes.
        </p>
        {choose}
      </div>
    </section>
  );
}
