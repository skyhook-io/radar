import { useMemo, useState } from "react";
import {
  type CapacityCertainty,
  type CapacityQuantityObservation,
  type CapacitySchedulingCapacity,
} from "@skyhook-io/k8s-ui";
import { Badge } from "@skyhook-io/k8s-ui/components/ui/Badge";
import {
  parseCPUToNanocores,
  parseMemoryToBytes,
} from "@skyhook-io/k8s-ui/utils/format";
import {
  CertaintyGlyph,
  LinkButton,
  SectionCard,
  certaintyGlyph,
  certaintyValueLabel,
  formatQuantity,
  quantityResourceRank,
  resourceLabel,
} from "./shared";

// Karpenter scheduling capacity — the fleet-level "how full is Karpenter, and
// how much is knocking at the door" bar. Karpenter-pooled nodes only: on mixed
// clusters, static capacity is deliberately outside this bar's ledger. Scale
// semantics are deliberate and asymmetric:
//   · scheduled requests fill the allocatable track proportionally;
//   · in-flight claim capacity extends BEYOND the allocatable edge, to scale —
//     it is capacity that will exist once claims register, never current
//     capacity, so it must not sit inside the track;
//   · pending demand is a COUNT chip after a `//` discontinuity — at 10×
//     overflow no bounded bar can draw it proportionally without lying, so
//     nothing after the `//` is to scale.
// The bar shows scheduling capacity (requests vs allocatable), never usage,
// and never acquires health colors.

type RowComponent = "requested" | "allocatable" | "inFlight" | "pending";

const COMPONENT_LABEL: Record<RowComponent, string> = {
  requested: "requested",
  allocatable: "allocatable",
  inFlight: "in flight",
  pending: "pending demand",
};

const CERTAINTY_BADNESS: Record<CapacityCertainty, number> = {
  exact: 0,
  lower_bound: 1,
  upper_bound: 2,
  unknown: 3,
};

export interface SchedulingBarRow {
  resource: string;
  /** Raw quantity per component; null = that source was not observed
   *  (unavailable ≠ zero). A present observation missing this resource is a
   *  structural "0". In-flight is the exception: null just means no claims
   *  are in flight. */
  requested: string | null;
  allocatable: string | null;
  inFlight: string | null;
  pending: string | null;
  /** Worst certainty across the components that determine the row; unknown
   *  when a load-bearing source (requested/allocatable/pending) is absent. */
  certainty: CapacityCertainty;
  /** Per-component certainty of present values, for glyph decomposition. */
  components: { component: RowComponent; certainty: CapacityCertainty }[];
  /** requested/allocatable in [0,1], null when not computable. */
  requestedFraction: number | null;
  /** in-flight relative to allocatable (same scale as the track), null when
   *  not computable or no in-flight. */
  inFlightFraction: number | null;
  /** Requests from bound pods with priority < 0 — the SUBSET of `requested`
   *  naming potential preemption victims. null = the field was absent (none
   *  observed or the source was unobserved); a present-but-missing resource is
   *  a structural "0". Never additive with `requested`. */
  negativePriority: string | null;
  /** Certainty of the negative-priority observation, for the annotation glyph;
   *  null when the field is absent. */
  negativePriorityCertainty: CapacityCertainty | null;
  /** negativePriority/allocatable in [0,1], populated ONLY when both the
   *  negative-priority and allocatable observations are exact — two lower
   *  bounds cannot establish a share. null otherwise (the row falls back to a
   *  text annotation). */
  negativePriorityFraction: number | null;
}

export function quantityToNumber(
  resource: string,
  value: string,
): number | null {
  if (resource === "cpu") {
    const parsed = parseCPUToNanocores(value);
    if (Number.isFinite(parsed) && (parsed !== 0 || /^[0.]+\D*$/.test(value))) {
      return parsed;
    }
    return null;
  }
  // Milli quantities before the byte parser — it ignores the `m` suffix and
  // would read 3000m as 3000, a 1000× distortion.
  const milli = /^(-?\d+(?:\.\d+)?)m$/.exec(value);
  if (milli) return Number(milli[1]) / 1000;
  const parsed = parseMemoryToBytes(value);
  if (Number.isFinite(parsed) && (parsed !== 0 || /^[0.]+\D*$/.test(value))) {
    return parsed;
  }
  const plain = Number(value);
  return Number.isFinite(plain) ? plain : null;
}

function worstCertainty(
  values: (CapacityCertainty | null)[],
): CapacityCertainty {
  let worst: CapacityCertainty = "exact";
  for (const value of values) {
    const certainty = value ?? "unknown";
    if (CERTAINTY_BADNESS[certainty] > CERTAINTY_BADNESS[worst]) {
      worst = certainty;
    }
  }
  return worst;
}

export function deriveSchedulingRows(
  scheduling: CapacitySchedulingCapacity | undefined,
  pending: CapacityQuantityObservation | undefined,
): SchedulingBarRow[] {
  const observations: Record<
    RowComponent,
    CapacityQuantityObservation | undefined
  > = {
    requested: scheduling?.scheduledRequests,
    allocatable: scheduling?.allocatable,
    inFlight: scheduling?.inFlightCapacity,
    pending,
  };
  const names = new Set<string>(["cpu", "memory"]);
  for (const observation of Object.values(observations)) {
    for (const name of Object.keys(observation?.resources ?? {})) {
      names.add(name);
    }
  }
  // Pod slots stay a chip in the pending-requests row — a bar row for a
  // resource nobody reasons about in units would be noise.
  names.delete("pods");

  const negativeObservation = scheduling?.negativePriorityRequests;

  const value = (component: RowComponent, resource: string): string | null => {
    const observation = observations[component];
    if (!observation) return null;
    return observation.resources[resource] ?? "0";
  };
  const numeric = (raw: string | null, resource: string): number | null =>
    raw === null ? null : quantityToNumber(resource, raw);

  const rows: SchedulingBarRow[] = [];
  for (const resource of names) {
    const requested = value("requested", resource);
    const allocatable = value("allocatable", resource);
    const pendingValue = value("pending", resource);
    let inFlight = value("inFlight", resource);
    const inFlightNumber = numeric(inFlight, resource);
    if (inFlightNumber !== null && inFlightNumber <= 0) inFlight = null;

    const allocatableNumber = numeric(allocatable, resource);
    const requestedNumber = numeric(requested, resource);
    const pendingNumber = numeric(pendingValue, resource);
    const negativePriority = negativeObservation
      ? (negativeObservation.resources[resource] ?? "0")
      : null;
    const negativePriorityNumber = numeric(negativePriority, resource);
    // A proportional split is only honest when the share's numerator and
    // denominator are both exact — two lower bounds cannot establish a fraction.
    const negativePrioritySplittable =
      negativeObservation?.certainty === "exact" &&
      observations.allocatable?.certainty === "exact";
    // A row earns its place with a signal: capacity that exists, requests or
    // demand that exist, in-flight claims, or an unobserved source worth an
    // honest "not zero". A fully-zero row (empty fleet, nothing pending) is
    // noise — the card-level empty state covers that story.
    const hasSignal =
      allocatable === null ||
      (allocatableNumber ?? 0) > 0 ||
      (requestedNumber ?? 0) > 0 ||
      (pendingNumber ?? 0) > 0 ||
      inFlight !== null;
    if (!hasSignal) continue;

    const components: SchedulingBarRow["components"] = [];
    for (const component of [
      "requested",
      "allocatable",
      "inFlight",
      "pending",
    ] as const) {
      const observation = observations[component];
      if (observation) {
        components.push({ component, certainty: observation.certainty });
      }
    }
    rows.push({
      resource,
      requested,
      allocatable,
      inFlight,
      pending: pendingValue,
      certainty: worstCertainty([
        observations.requested?.certainty ?? null,
        observations.allocatable?.certainty ?? null,
        observations.pending?.certainty ?? null,
        // Absent in-flight means "none", not "unobserved" — it never
        // degrades the row.
        inFlight === null
          ? "exact"
          : (observations.inFlight?.certainty ?? null),
      ]),
      components,
      requestedFraction:
        requestedNumber !== null &&
        allocatableNumber !== null &&
        allocatableNumber > 0
          ? Math.min(1, requestedNumber / allocatableNumber)
          : null,
      inFlightFraction:
        inFlightNumber !== null &&
        inFlightNumber > 0 &&
        allocatableNumber !== null &&
        allocatableNumber > 0
          ? inFlightNumber / allocatableNumber
          : null,
      negativePriority,
      negativePriorityCertainty: negativeObservation?.certainty ?? null,
      negativePriorityFraction:
        negativePrioritySplittable &&
        negativePriorityNumber !== null &&
        negativePriorityNumber > 0 &&
        allocatableNumber !== null &&
        allocatableNumber > 0
          ? Math.min(1, negativePriorityNumber / allocatableNumber)
          : null,
    });
  }
  rows.sort(
    (left, right) =>
      quantityResourceRank(left.resource) -
        quantityResourceRank(right.resource) ||
      left.resource.localeCompare(right.resource),
  );
  return rows;
}

const VISIBLE_ROW_LIMIT = 3;

// The bar has one explicit scope. Cluster scope draws scheduled requests vs
// allocatable across EVERY observed node (all managers); Karpenter scope keeps
// the original Karpenter-pooled-only ledger. In-flight capacity is Karpenter-
// only in both — no other manager reports claim capacity before it registers —
// so the cluster legend spells that out.
type SchedulingScope = "cluster" | "karpenter";

const SCOPE_COPY: Record<
  SchedulingScope,
  { title: string; subtitle: string; inFlightLegend: string; empty: string }
> = {
  cluster: {
    title: "Cluster scheduling capacity",
    subtitle:
      "scheduled requests vs node allocatable, all observed nodes · not usage, not a health score",
    inFlightLegend: "in flight (Karpenter, beyond the edge)",
    empty:
      "No scheduling capacity observed yet — no node reports allocatable, and nothing is pending. Bars appear as soon as either side of the ledger exists.",
  },
  karpenter: {
    title: "Karpenter scheduling capacity",
    subtitle:
      "scheduled requests vs node allocatable, Karpenter-pooled nodes only · not usage, not a health score",
    inFlightLegend: "in flight (beyond the edge)",
    empty:
      "No Karpenter-provisioned capacity yet — the fleet has no pooled nodes or in-flight claims, and no pending demand. Bars appear as soon as either side of the ledger exists.",
  },
};

export function ClusterSchedulingCard({
  scheduling,
  pending,
  onExplain,
  scope = "karpenter",
}: {
  scheduling?: CapacitySchedulingCapacity;
  pending?: CapacityQuantityObservation;
  onExplain: () => void;
  scope?: SchedulingScope;
}) {
  const rows = useMemo(
    () => deriveSchedulingRows(scheduling, pending),
    [scheduling, pending],
  );
  const [expanded, setExpanded] = useState(false);
  if (!scheduling) return null;
  const copy = SCOPE_COPY[scope];
  const visible = expanded ? rows : rows.slice(0, VISIBLE_ROW_LIMIT);
  const hidden = rows.length - visible.length;
  // The hatch legend swatch describes a drawn sub-band; under non-exact
  // certainty the negative-priority value is a text annotation instead, which is
  // self-describing, so the swatch would describe nothing.
  const hasNegativePriorityBand = rows.some(
    (row) => row.negativePriorityFraction !== null,
  );

  if (rows.length === 0) {
    return (
      <SectionCard
        title={copy.title}
        subtitle={copy.subtitle}
        actions={<LinkButton onClick={onExplain}>How to read →</LinkButton>}
        bodyClassName="px-4 py-3"
      >
        <p className="text-xs text-theme-text-tertiary">{copy.empty}</p>
      </SectionCard>
    );
  }

  return (
    <SectionCard
      title={copy.title}
      subtitle={copy.subtitle}
      actions={<LinkButton onClick={onExplain}>How to read →</LinkButton>}
      bodyClassName="flex flex-col gap-3 px-4 py-3"
    >
      {visible.map((row) => (
        <SchedulingBarRowView key={row.resource} row={row} />
      ))}
      {hidden > 0 && (
        <LinkButton onClick={() => setExpanded(true)}>
          +{hidden} more {hidden === 1 ? "resource" : "resources"} ▾
        </LinkButton>
      )}
      {expanded && rows.length > VISIBLE_ROW_LIMIT && (
        <LinkButton onClick={() => setExpanded(false)}>Show less ▴</LinkButton>
      )}
      <div className="flex flex-wrap items-center gap-x-4 gap-y-1 border-t border-theme-border-subtle pt-2 text-[11px] text-theme-text-tertiary">
        <span className="flex items-center gap-1.5">
          <span aria-hidden className="h-2 w-4 rounded-sm bg-blue-500/70" />
          scheduled requests
        </span>
        <span className="flex items-center gap-1.5">
          <span
            aria-hidden
            className="h-2 w-4 rounded-sm border border-theme-border"
            style={HATCH_STYLE}
          />
          {copy.inFlightLegend}
        </span>
        <span className="flex items-center gap-1.5">
          <span
            aria-hidden
            className="h-2 w-4 rounded-sm border border-dashed border-orange-400"
          />
          pending demand — count, not to scale
        </span>
        {hasNegativePriorityBand && (
          <span className="flex items-center gap-1.5">
            <span
              aria-hidden
              className="h-2 w-4 rounded-sm bg-blue-500/70 text-theme-text-primary"
              style={NEGATIVE_PRIORITY_HATCH_STYLE}
            />
            negative-priority (potential preemption victims)
          </span>
        )}
      </div>
    </SectionCard>
  );
}

// currentColor hatch keeps the pattern legible on both themes without
// hand-picking a color pair.
const HATCH_STYLE = {
  backgroundImage:
    "repeating-linear-gradient(135deg, currentColor 0 1px, transparent 1px 5px)",
} as const;

// A denser, opposite-angle hatch distinguishes the negative-priority sub-band
// from the in-flight hatch and the solid requested fill it sits inside. Same
// currentColor approach — no hand-picked color pair.
const NEGATIVE_PRIORITY_HATCH_STYLE = {
  backgroundImage:
    "repeating-linear-gradient(45deg, currentColor 0 1px, transparent 1px 4px)",
} as const;

function rowGlyphTitle(row: SchedulingBarRow): string {
  const parts = row.components.map(
    ({ component, certainty }) =>
      `${COMPONENT_LABEL[component]} ${certaintyGlyph(certainty)} ${certaintyValueLabel(certainty).toLowerCase()}`,
  );
  return `Worst-case certainty across this row. ${parts.join(" · ")}.`;
}

function SchedulingBarRowView({ row }: { row: SchedulingBarRow }) {
  const pendingNumber =
    row.pending === null ? null : quantityToNumber(row.resource, row.pending);
  const showPendingChip = pendingNumber !== null && pendingNumber > 0;
  const negativePriorityNumber =
    row.negativePriority === null
      ? null
      : quantityToNumber(row.resource, row.negativePriority);
  // Band XOR annotation: the exact case draws the proportional sub-band; any
  // present-but-unsplittable value falls back to an honest text annotation whose
  // glyph carries the certainty (≥ under a lower bound).
  const showNegativeAnnotation =
    row.negativePriorityFraction === null &&
    negativePriorityNumber !== null &&
    negativePriorityNumber > 0;
  const ariaLabel = [
    resourceLabel(row.resource),
    row.requested !== null
      ? `${formatQuantity(row.resource, row.requested)} requested`
      : "requested unknown",
    row.allocatable !== null
      ? `of ${formatQuantity(row.resource, row.allocatable)} allocatable`
      : "allocatable unknown",
    row.inFlight !== null
      ? `${formatQuantity(row.resource, row.inFlight)} in flight`
      : null,
    row.pending !== null
      ? `${formatQuantity(row.resource, row.pending)} pending demand`
      : "pending demand unknown",
    row.negativePriority !== null &&
    negativePriorityNumber !== null &&
    negativePriorityNumber > 0
      ? `${formatQuantity(row.resource, row.negativePriority)} negative-priority`
      : null,
  ]
    .filter(Boolean)
    .join(", ");

  return (
    <div role="group" aria-label={ariaLabel}>
      <div className="flex items-center gap-3">
        <span className="flex w-44 shrink-0 items-center gap-1.5">
          <span className="truncate font-mono text-xs text-theme-text-primary">
            {row.resource === "cpu" || row.resource === "memory"
              ? resourceLabel(row.resource)
              : row.resource}
          </span>
          {row.certainty !== "exact" && (
            <CertaintyGlyph
              certainty={row.certainty}
              title={rowGlyphTitle(row)}
            />
          )}
        </span>
        {row.allocatable === null ? (
          <span className="text-xs text-theme-text-tertiary">
            Node inventory not observed — capacity unknown, not zero.
          </span>
        ) : (
          <div className="flex min-w-0 flex-1 items-center">
            <div
              className="relative h-2.5 min-w-3 overflow-hidden rounded-l-full rounded-r-none border border-theme-border bg-theme-elevated"
              style={{
                flexGrow: Math.max(
                  quantityToNumber(row.resource, row.allocatable) ?? 0,
                  0,
                ),
                flexBasis: 12,
                borderTopRightRadius: row.inFlightFraction ? 0 : undefined,
                borderBottomRightRadius: row.inFlightFraction ? 0 : undefined,
              }}
            >
              {row.requestedFraction !== null && (
                <div
                  className="flex h-full justify-end bg-blue-500/70"
                  style={{ width: `${row.requestedFraction * 100}%` }}
                >
                  {row.negativePriorityFraction !== null && (
                    <div
                      aria-hidden
                      className="h-full border-l border-theme-base text-theme-text-primary"
                      style={{
                        ...NEGATIVE_PRIORITY_HATCH_STYLE,
                        width: `${
                          Math.min(
                            1,
                            row.negativePriorityFraction /
                              row.requestedFraction,
                          ) * 100
                        }%`,
                      }}
                    />
                  )}
                </div>
              )}
            </div>
            {row.inFlightFraction !== null && (
              <div
                aria-hidden
                className="h-2.5 rounded-r-full border border-l-2 border-theme-border border-l-theme-text-tertiary text-theme-text-tertiary"
                style={{
                  ...HATCH_STYLE,
                  flexGrow:
                    (quantityToNumber(row.resource, row.allocatable) ?? 0) *
                    row.inFlightFraction,
                  flexBasis: 8,
                }}
              />
            )}
            {showPendingChip && (
              <>
                <span
                  aria-hidden
                  className="shrink-0 px-1.5 font-mono text-xs text-theme-text-tertiary"
                >
                  {"//"}
                </span>
                <Badge
                  severity="alert"
                  size="sm"
                  className="shrink-0 border-dashed font-mono"
                >
                  +{formatQuantity(row.resource, row.pending ?? "")} pending
                </Badge>
              </>
            )}
          </div>
        )}
      </div>
      <div className="ml-[11.75rem] mt-1 flex flex-wrap items-baseline gap-x-3 gap-y-0.5 text-xs">
        <NumberPart
          value={
            row.requested !== null
              ? formatQuantity(row.resource, row.requested)
              : null
          }
          label="requested"
          missing="requested unavailable — not zero"
        />
        <NumberPart
          value={
            row.allocatable !== null
              ? formatQuantity(row.resource, row.allocatable)
              : null
          }
          label="allocatable"
          prefix="of"
          missing="allocatable unavailable — not zero"
        />
        {row.inFlight !== null && (
          <NumberPart
            value={`+${formatQuantity(row.resource, row.inFlight)}`}
            label="in flight"
          />
        )}
        {row.pending !== null &&
          pendingNumber !== null &&
          pendingNumber > 0 && (
            <span className="text-orange-700 dark:text-orange-400">
              <span className="font-mono">
                {formatQuantity(row.resource, row.pending)}
              </span>{" "}
              pending demand
            </span>
          )}
        {showNegativeAnnotation && row.negativePriority !== null && (
          <span className="text-theme-text-tertiary">
            <span className="font-mono text-theme-text-secondary">
              {`${certaintyGlyph(
                row.negativePriorityCertainty ?? "unknown",
              )}${formatQuantity(row.resource, row.negativePriority)}`}
            </span>{" "}
            negative-priority
          </span>
        )}
      </div>
    </div>
  );
}

function NumberPart({
  value,
  label,
  prefix,
  missing,
}: {
  value: string | null;
  label: string;
  prefix?: string;
  missing?: string;
}) {
  if (value === null) {
    return missing ? (
      <span className="text-theme-text-tertiary">{missing}</span>
    ) : null;
  }
  return (
    <span className="text-theme-text-secondary">
      {prefix ? `${prefix} ` : ""}
      <span className="font-mono text-theme-text-primary">{value}</span>{" "}
      <span className="text-theme-text-tertiary">{label}</span>
    </span>
  );
}
