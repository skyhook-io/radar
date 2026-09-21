import { useContext } from "react";
import { clsx } from "clsx";
import { Info } from "lucide-react";
import {
  Badge,
  StatusDot,
  TerminalBlock,
  displayKind,
  HPADiagnosisSummary,
  ResourceLink,
} from "@skyhook-io/k8s-ui";
import type {
  DiagnosisResourceContext,
  DiagnosisResourceRef,
  DiagnosisScalerRef,
} from "../../diagnoseEvidenceTypes";
import { InvestigationResourceEvidence } from "../../InvestigationResourceEvidence";
import { EvidenceNavigationContext } from "../navigation";
import {
  type EvidenceDataOf,
  ResourceFact,
  conditionStatusTone,
  gitOpsValueSeverity,
  severityBadge,
} from "../cardParts";

export function IssueBody({
  data,
  cardSummary,
}: {
  data: EvidenceDataOf<"issue">;
  cardSummary?: string;
}) {
  const issue = data.issue;
  const showCause =
    Boolean(issue.cause) && issue.cause?.trim() !== cardSummary?.trim();
  const showMessage =
    Boolean(issue.message) &&
    issue.message?.trim() !== cardSummary?.trim() &&
    issue.message?.trim() !== issue.cause?.trim();
  return (
    <div className="space-y-2">
      <div className="flex flex-wrap gap-1.5">
        <Badge
          severity={issue.severity === "critical" ? "error" : "warning"}
          size="sm"
        >
          {issue.severity}
        </Badge>
        <Badge tone="structural" size="sm">
          {issue.kind}
        </Badge>
        <Badge tone="structural" size="sm">
          {issue.namespace ? `${issue.namespace}/` : ""}
          {issue.name}
        </Badge>
        {data.pods && data.pods.length > 0 ? (
          <>
            <Badge tone="structural" size="sm">
              {data.pods.length === 1 ? "1 Pod" : `${data.pods.length} Pods`}
            </Badge>
            {data.pods.map((pod) => (
              <Badge key={pod} tone="structural" size="sm">
                {pod}
              </Badge>
            ))}
          </>
        ) : null}
      </div>
      {showCause ? (
        <p className="text-sm font-medium leading-relaxed text-theme-text-primary">
          {issue.cause}
        </p>
      ) : null}
      {showMessage ? (
        <p className="text-xs leading-relaxed text-theme-text-secondary">
          {issue.message}
        </p>
      ) : null}
    </div>
  );
}

export function StartupBody({ data }: { data: EvidenceDataOf<"startup"> }) {
  const blocker = data.blocker;
  const pods = data.pods && data.pods.length > 1 ? data.pods : [blocker.name];
  return (
    <div className="space-y-2">
      <div className="flex flex-wrap gap-1.5">
        <Badge tone="structural" size="sm">
          {pods.length > 1 ? `${pods.length} ${blocker.kind}s` : blocker.kind}
        </Badge>
        {pods.map((pod) => (
          <Badge key={pod} tone="structural" size="sm">
            {pod}
          </Badge>
        ))}
        <Badge severity={severityBadge(blocker.severity)} size="sm">
          {blocker.severity}
        </Badge>
      </div>
      <p className="text-sm leading-relaxed text-theme-text-primary">
        {blocker.message}
      </p>
    </div>
  );
}

export function CrashBody({ data }: { data: EvidenceDataOf<"crash"> }) {
  const crash = data.crash;
  return (
    <div className="space-y-2.5">
      <div className="flex flex-wrap gap-1.5">
        <Badge severity="error" size="sm">
          {crash.reason || crash.state}
        </Badge>
        <Badge tone="structural" size="sm">
          exit {crash.exitCode}
        </Badge>
        <Badge tone="structural" size="sm">
          {crash.container}
        </Badge>
      </div>
      <p className="text-xs text-theme-text-tertiary">
        {crash.pods.join(", ")} · {crash.logSource.replaceAll("_", " ")}
      </p>
      <TerminalBlock label="Selected crash line">{crash.logLine}</TerminalBlock>
    </div>
  );
}

export function ResourceBody({ data }: { data: EvidenceDataOf<"resource"> }) {
  const replicas = data.resourceContext?.workloadSummary?.replicas;
  // SealedSecret's dedicated body renders the resource's conditions beside
  // its controller state, so repeating the derived summary here adds noise.
  const conditions =
    data.resource.kind.toLowerCase() === "sealedsecret"
      ? []
      : (data.resourceContext?.statusSummary?.conditions ?? []);
  const desired = replicas?.desired;
  const ready = replicas ? (replicas.ready ?? 0) : undefined;
  const shortfall =
    desired !== undefined && ready !== undefined && ready < desired;
  const scalers = diagnosedScalers(data.resourceContext);
  return (
    <div className="space-y-3">
      <InvestigationResourceEvidence resource={data.resource} />
      {data.gitOpsDiagnosis ? (
        <GitOpsStatusBody status={data.gitOpsDiagnosis} />
      ) : null}
      {desired !== undefined && ready !== undefined ? (
        <dl className="flex flex-wrap gap-x-6 gap-y-2 text-xs">
          <div>
            <dt className="text-theme-text-tertiary">Ready replicas</dt>
            <dd
              className={clsx(
                "font-mono font-semibold tabular-nums",
                shortfall ? "text-warning-text" : "text-theme-text-primary",
              )}
            >
              {ready}/{desired}
            </dd>
          </div>
          <ResourceFact label="Available" value={replicas?.available} />
          <ResourceFact label="Updated" value={replicas?.updated} />
          <ResourceFact label="Unavailable" value={replicas?.unavailable} />
          <ResourceFact
            label="Phase"
            value={data.resourceContext?.statusSummary?.phase}
          />
        </dl>
      ) : null}
      {conditions.length > 0 ? (
        <div>
          <div className="mb-1.5 text-xs font-semibold uppercase tracking-wide text-theme-text-tertiary">
            Conditions
          </div>
          <div className="max-h-52 space-y-1.5 overflow-y-auto pr-1">
            {conditions.map((condition) => (
              <div
                key={`${condition.type}-${condition.status}-${condition.reason ?? ""}`}
                className="flex min-w-0 items-start gap-2 text-xs"
              >
                <StatusDot
                  tone={conditionStatusTone(condition)}
                  className="mt-1 shrink-0"
                />
                <p className="min-w-0 text-theme-text-secondary">
                  <span className="font-medium text-theme-text-primary">
                    {condition.type}={condition.status}
                  </span>
                  {condition.reason ? ` · ${condition.reason}` : ""}
                  {condition.message ? ` — ${condition.message}` : ""}
                </p>
              </div>
            ))}
          </div>
        </div>
      ) : null}
      {scalers.length > 0 ? (
        <ScaledBySection
          scalers={scalers}
          allScalers={data.resourceContext?.scaledBy ?? []}
        />
      ) : null}
      {data.warnings.length > 0 ? (
        <ul className="space-y-1 text-xs text-theme-text-secondary">
          {data.warnings.map((warning) => (
            <li key={warning} className="flex items-start gap-1.5">
              <Info className="mt-0.5 h-3.5 w-3.5 shrink-0 text-theme-text-tertiary" />
              <span>{warning}</span>
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}

type DiagnosedScaler = DiagnosisScalerRef &
  Required<Pick<DiagnosisScalerRef, "hpaSummary">>;

export function diagnosedScalers(
  context: DiagnosisResourceContext | undefined,
): DiagnosedScaler[] {
  return (context?.scaledBy ?? []).filter(
    (scaler): scaler is DiagnosedScaler => scaler.hpaSummary !== undefined,
  );
}

function ScaledBySection({
  scalers,
  allScalers,
}: {
  scalers: DiagnosedScaler[];
  allScalers: DiagnosisScalerRef[];
}) {
  const { onOpenResource } = useContext(EvidenceNavigationContext);
  // The ScaledObject is only a link when Radar listed it as a scaler too; a
  // name read off the HPA's owner reference alone is not a resource we hold.
  const listedScaler = (ref: DiagnosisResourceRef) =>
    allScalers.some(
      (scaler) =>
        scaler.kind === ref.kind &&
        scaler.name === ref.name &&
        (scaler.namespace ?? "") === (ref.namespace ?? ""),
    );
  return (
    <div>
      <div className="mb-1.5 text-xs font-semibold uppercase tracking-wide text-theme-text-tertiary">
        Scaled by
      </div>
      <div className="space-y-2">
        {scalers.map((scaler) => (
          <HPADiagnosisSummary
            key={`${scaler.namespace ?? ""}/${scaler.name}`}
            diagnosis={scaler.hpaSummary}
            variant="inline"
            header={
              <>
                <span className="font-medium text-theme-text-primary">
                  {displayKind(scaler.kind)} {scaler.name}
                </span>
                {scaler.managedBy ? (
                  <span className="text-theme-text-tertiary">
                    managed by KEDA {displayKind(scaler.managedBy.kind)}{" "}
                    <ResourceLink
                      name={scaler.managedBy.name}
                      kind={scaler.managedBy.kind}
                      namespace={scaler.managedBy.namespace ?? ""}
                      group={scaler.managedBy.group}
                      onNavigate={
                        onOpenResource && listedScaler(scaler.managedBy)
                          ? (ref) => onOpenResource(ref)
                          : undefined
                      }
                    />
                  </span>
                ) : null}
              </>
            }
          />
        ))}
      </div>
    </div>
  );
}

function GitOpsStatusBody({
  status,
}: {
  status: NonNullable<EvidenceDataOf<"resource">["gitOpsDiagnosis"]>;
}) {
  const fields = [
    ["Sync", status.sync],
    ["Health", status.health],
    ["Operation", status.operationPhase],
    ["Ready", status.ready],
  ] as const;
  return (
    <div className="rounded-md border border-theme-border bg-theme-base/40 p-2.5">
      <div className="mb-2 flex flex-wrap items-center gap-1.5">
        <span className="text-xs font-semibold uppercase tracking-wide text-theme-text-tertiary">
          GitOps controller status
        </span>
        <Badge tone="note" size="sm">
          {status.tool === "argocd" ? "Argo CD" : "Flux"}
        </Badge>
        {status.suspended ? (
          <Badge severity="info" size="sm">
            Suspended
          </Badge>
        ) : null}
      </div>
      <div className="flex flex-wrap gap-1.5">
        {fields.map(([label, value]) =>
          value ? (
            <Badge
              key={label}
              severity={gitOpsValueSeverity(label, value)}
              size="sm"
            >
              {label}: {value}
            </Badge>
          ) : null,
        )}
        {status.appliedRevision ? (
          <Badge tone="structural" size="sm">
            {status.appliedRevision}
          </Badge>
        ) : null}
      </div>
    </div>
  );
}
