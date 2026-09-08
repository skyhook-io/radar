import {
  Activity,
  BellRing,
  Boxes,
  Bug,
  ChartLine,
  CheckCircle2,
  CircleAlert,
  Clock3,
  FileClock,
  KeyRound,
  ListTree,
  Network,
  Package,
  ScrollText,
  ShieldAlert,
  type LucideIcon,
} from "lucide-react";

import type { InvestigationEvidenceKind } from "./investigationEvidence";

/**
 * Everything the pane needs to know about a kind of evidence, in one place.
 *
 * These decisions used to live in eight unrelated lists across four files, and
 * adding a kind meant remembering all eight: the round that added alerts and
 * Helm cards missed the one that decides whether a card can contradict a
 * model's all-clear, so a firing alert naming the workload sat under a green
 * "no problem found" banner. Because this is a `Record` over the kind union,
 * the compiler now refuses a new kind until every decision below is made for
 * it.
 */
export interface InvestigationEvidenceKindTraits {
  /**
   * A card of this kind can contradict an all-clear. Radar captured a fact
   * about the investigated resource that says something is wrong right now,
   * so a model concluding otherwise has to be qualified rather than trusted.
   * False for kinds that describe context, configuration or a shape of the
   * cluster: those are worth reading and are not evidence of a live problem.
   */
  adverse: boolean;
  /**
   * One card of this kind is one unambiguous subject, so citing its source
   * promotes exactly that fact. A broad read (issues, inventory, events)
   * yields many rows from one source and a citation cannot pick one of them.
   */
  focused: boolean;
  /** The card carries enough detail to want the full width of a grid row. */
  fullRow: boolean;
  icon: LucideIcon;
  /**
   * One sentence about what this kind of evidence can and cannot establish,
   * shown under the card. `changes` has its own tooltip instead, because its
   * wording depends on whether the age was collected.
   */
  caveat?: string;
}

export const EVIDENCE_KIND_TRAITS: Readonly<
  Record<InvestigationEvidenceKind, InvestigationEvidenceKindTraits>
> = {
  // Radar classified a live problem on the resource.
  issue: { adverse: true, focused: false, fullRow: false, icon: CircleAlert },
  // A pod of the workload cannot start.
  startup: { adverse: true, focused: false, fullRow: false, icon: ShieldAlert },
  // A container exited or is restarting.
  crash: { adverse: true, focused: true, fullRow: false, icon: Bug },
  // The object's own status: unready replicas, a failed condition.
  resource: { adverse: true, focused: true, fullRow: false, icon: Boxes },
  logs: {
    adverse: true,
    focused: true,
    fullRow: true,
    icon: ScrollText,
  },
  events: {
    adverse: true,
    focused: false,
    fullRow: true,
    icon: Clock3,
    caveat:
      "Events support the timeline; proximity alone does not establish cause.",
  },
  // A change is a thing that happened, not a thing that is wrong.
  changes: { adverse: false, focused: false, fullRow: false, icon: FileClock },
  dns: { adverse: true, focused: false, fullRow: false, icon: Activity },
  network: { adverse: true, focused: false, fullRow: false, icon: Network },
  relationships: {
    adverse: false,
    focused: false,
    fullRow: false,
    icon: Network,
    caveat:
      "This shows direct relationships Radar found, not an inferred blast radius.",
  },
  topology: {
    adverse: false,
    focused: false,
    fullRow: false,
    icon: Network,
    caveat:
      "This shows direct relationships Radar found, not an inferred blast radius.",
  },
  inventory: { adverse: false, focused: false, fullRow: false, icon: ListTree },
  // A receipt records a check that found nothing, so it can never contradict.
  receipt: {
    adverse: false,
    focused: false,
    fullRow: false,
    icon: CheckCircle2,
  },
  // A rule firing about this workload is the most literal statement Radar
  // holds that something is wrong with it.
  alerts: {
    adverse: true,
    focused: true,
    fullRow: true,
    icon: BellRing,
    caveat:
      "Instances are matched to this investigation by their Prometheus labels; a firing rule alone does not establish the cause.",
  },
  // A failed or stuck release is a live problem with the thing that owns the
  // workload, not background colour.
  helm: { adverse: true, focused: true, fullRow: false, icon: Package },
  // A permission verdict describes a grant, not a runtime failure: a
  // ServiceAccount that cannot read Secrets may be exactly as intended.
  permissions: {
    adverse: false,
    focused: true,
    fullRow: false,
    icon: KeyRound,
    caveat:
      "This is what RBAC grants the subject, not what the workload has exercised.",
  },
  // A chart is a measurement; the reading, not the kind, is what alarms.
  metrics: { adverse: false, focused: true, fullRow: true, icon: ChartLine },
};

/** Kinds whose card can contradict a model's all-clear. */
export function evidenceKindIsAdverse(kind: string): boolean {
  return (
    Object.hasOwn(EVIDENCE_KIND_TRAITS, kind) &&
    EVIDENCE_KIND_TRAITS[kind as InvestigationEvidenceKind].adverse
  );
}

/** Kinds a citation of the source can promote as one fact. */
export function evidenceKindIsFocused(kind: string): boolean {
  return (
    Object.hasOwn(EVIDENCE_KIND_TRAITS, kind) &&
    EVIDENCE_KIND_TRAITS[kind as InvestigationEvidenceKind].focused
  );
}
