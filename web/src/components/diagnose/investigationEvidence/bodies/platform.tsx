import { useContext } from "react";
import { clsx } from "clsx";
import {
  Badge,
  StatusDot,
  formatRelativeAgeTime,
  mapHealthToTone,
  ResourceLink,
} from "@skyhook-io/k8s-ui";
import { apiVersionToGroup } from "../../../../utils/navigation";
import { Tooltip } from "../../../ui/Tooltip";
import { EvidenceNavigationContext } from "../navigation";
import { type EvidenceDataOf, ResourceFact } from "../cardParts";

function helmStatusSeverity(status: string) {
  const normalized = status.toLowerCase();
  if (normalized === "deployed") return "success" as const;
  if (normalized.includes("failed")) return "error" as const;
  if (normalized.startsWith("pending") || normalized === "uninstalling")
    return "info" as const;
  return "warning" as const;
}

export function HelmBody({ data }: { data: EvidenceDataOf<"helm"> }) {
  const { onOpenResource } = useContext(EvidenceNavigationContext);
  const { release } = data;
  const operation = release.lastOperation;
  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-1.5">
        <Badge severity={helmStatusSeverity(release.status)} size="sm">
          {release.status}
        </Badge>
        <Badge tone="structural" size="sm">
          {release.chart}
          {release.chartVersion ? ` ${release.chartVersion}` : ""}
        </Badge>
        <Badge tone="structural" size="sm">
          revision {release.revision}
        </Badge>
        {release.appVersion ? (
          <Badge tone="note" size="sm">
            app {release.appVersion}
          </Badge>
        ) : null}
        {release.resourceHealth ? (
          <Badge
            severity={
              mapHealthToTone(release.resourceHealth) === "healthy"
                ? "success"
                : mapHealthToTone(release.resourceHealth) === "unhealthy"
                  ? "error"
                  : "warning"
            }
            size="sm"
          >
            resources {release.resourceHealth}
          </Badge>
        ) : null}
        <Tooltip
          content={new Date(release.updated).toLocaleString()}
          delay={150}
          position="left"
          wrapperClassName="ml-auto"
        >
          <time
            dateTime={release.updated}
            className="text-xs text-theme-text-tertiary"
          >
            updated {formatRelativeAgeTime(release.updated)}
          </time>
        </Tooltip>
      </div>
      {release.healthIssue ? (
        <p className="text-xs leading-relaxed text-warning-text">
          {release.healthIssue}
        </p>
      ) : null}
      {release.healthSummary &&
      release.healthSummary !== release.healthIssue ? (
        <p className="text-xs leading-relaxed text-theme-text-secondary">
          {release.healthSummary}
        </p>
      ) : null}
      {operation ? (
        <div className="rounded-md border border-theme-border bg-theme-base/40 px-2.5 py-2">
          <div className="flex flex-wrap items-center gap-1.5">
            <span className="text-xs font-semibold uppercase tracking-wide text-theme-text-tertiary">
              Last operation
            </span>
            <Badge tone="note" size="sm">
              {operation.kind.replaceAll("_", " ")}
            </Badge>
            <Badge
              severity={
                operation.status === "completed"
                  ? "success"
                  : operation.status === "failed"
                    ? "error"
                    : "warning"
              }
              size="sm"
            >
              {operation.status.replaceAll("_", " ")}
            </Badge>
          </div>
          {operation.message ? (
            <p className="mt-1 text-xs leading-relaxed text-theme-text-secondary [overflow-wrap:anywhere]">
              {operation.message}
            </p>
          ) : null}
        </div>
      ) : null}
      {release.description ? (
        <p className="text-xs text-theme-text-tertiary [overflow-wrap:anywhere]">
          {release.description}
        </p>
      ) : null}
      {release.storageNamespace &&
      release.storageNamespace !== release.namespace ? (
        <p className="text-xs text-theme-text-tertiary">
          Release metadata stored in namespace {release.storageNamespace}
        </p>
      ) : null}
      {release.managedByFluxHelmRelease ? (
        <p className="text-xs text-theme-text-tertiary">
          Managed by Flux HelmRelease {release.managedByFluxHelmRelease}
        </p>
      ) : null}
      {release.resources.length > 0 ? (
        <div className="max-h-52 overflow-y-auto rounded-md border border-theme-border">
          {release.resources.map((owned, index) => (
            <div
              key={`${owned.kind}-${owned.namespace}-${owned.name}`}
              className={clsx(
                "flex min-w-0 items-center gap-2 px-2.5 py-1.5",
                index > 0 && "border-t border-theme-border/60",
              )}
            >
              <StatusDot
                tone={mapHealthToTone(
                  owned.issue ? "unhealthy" : (owned.status ?? ""),
                )}
                className="shrink-0"
              />
              <Badge tone="structural" size="sm">
                {owned.kind}
              </Badge>
              <span className="min-w-0 flex-1">
                <span className="block truncate font-mono text-xs">
                  <ResourceLink
                    name={owned.name}
                    kind={owned.kind}
                    namespace={owned.namespace}
                    group={
                      owned.apiVersion
                        ? apiVersionToGroup(owned.apiVersion)
                        : undefined
                    }
                    label={`${owned.namespace ? `${owned.namespace}/` : ""}${owned.name}`}
                    onNavigate={
                      owned.apiVersion && onOpenResource
                        ? (ref) => onOpenResource(ref)
                        : undefined
                    }
                  />
                </span>
                {owned.issue ? (
                  <span className="block truncate text-xs text-warning-text">
                    {owned.issue}
                  </span>
                ) : owned.summary || owned.message ? (
                  <span className="block truncate text-xs text-theme-text-tertiary">
                    {owned.summary || owned.message}
                  </span>
                ) : null}
              </span>
              {owned.ready || owned.status ? (
                <span className="ml-auto shrink-0 font-mono text-xs text-theme-text-tertiary">
                  {owned.ready || owned.status}
                </span>
              ) : null}
            </div>
          ))}
        </div>
      ) : null}
    </div>
  );
}

export function PermissionsBody({
  data,
}: {
  data: EvidenceDataOf<"permissions">;
}) {
  const { subject, accessCheck } = data;
  const subjectBadges = (
    <>
      <Badge tone="structural" size="sm">
        {subject.kind}
      </Badge>
      <span className="font-mono text-xs text-theme-text-secondary">
        {subject.namespace ? `${subject.namespace}/` : ""}
        {subject.name}
      </span>
    </>
  );
  if (accessCheck) {
    const facts = [
      ["Verb", accessCheck.verb],
      ["Resource", accessCheck.resource],
      ["Subresource", accessCheck.subresource],
      ["API group", accessCheck.group || "core"],
      ["Namespace", accessCheck.namespace || "cluster-wide"],
      ["Name", accessCheck.resourceName],
    ] as const;
    return (
      <div className="space-y-2.5">
        <div className="flex flex-wrap items-center gap-1.5">
          {subjectBadges}
          <Badge
            severity={accessCheck.allowed ? "success" : "warning"}
            size="sm"
          >
            {accessCheck.allowed
              ? "allowed"
              : accessCheck.denied
                ? "denied"
                : "not allowed"}
          </Badge>
        </div>
        <dl className="flex flex-wrap gap-x-6 gap-y-2 text-xs">
          {facts.map(([label, value]) => (
            <ResourceFact key={label} label={label} value={value} />
          ))}
        </dl>
        {accessCheck.reason ? (
          <p className="text-xs leading-relaxed text-theme-text-secondary [overflow-wrap:anywhere]">
            {accessCheck.reason}
          </p>
        ) : null}
        {accessCheck.evaluationError ? (
          <p className="text-xs leading-relaxed text-semantic-error">
            {accessCheck.evaluationError}
          </p>
        ) : null}
      </div>
    );
  }
  const bindings = data.bindings ?? [];
  const usedByPods = data.usedByPods ?? [];
  return (
    <div className="space-y-2.5">
      <div className="flex flex-wrap items-center gap-1.5">
        {subjectBadges}
        <Badge tone="note" size="sm">
          {data.flatRulesCount ?? 0}
          {data.truncated ? "+" : ""} effective rules
        </Badge>
        {data.truncated ? (
          <Badge severity="warning" size="sm">
            rule list truncated
          </Badge>
        ) : null}
      </div>
      {bindings.length > 0 ? (
        <div className="max-h-52 overflow-y-auto rounded-md border border-theme-border">
          {bindings.map((binding, index) => (
            <div
              key={`${binding.bindingKind}-${binding.bindingNamespace ?? ""}-${binding.bindingName}`}
              className={clsx(
                "flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 px-2.5 py-1.5 text-xs",
                index > 0 && "border-t border-theme-border/60",
              )}
            >
              <Badge tone="structural" size="sm">
                {binding.bindingKind}
              </Badge>
              <span className="min-w-0 truncate font-mono text-theme-text-secondary">
                {binding.bindingNamespace ? `${binding.bindingNamespace}/` : ""}
                {binding.bindingName}
              </span>
              <span className="text-theme-text-tertiary">→</span>
              <Badge tone="structural" size="sm">
                {binding.roleKind}
              </Badge>
              <span className="min-w-0 truncate font-mono text-theme-text-primary">
                {binding.roleNamespace ? `${binding.roleNamespace}/` : ""}
                {binding.roleName}
              </span>
              <span className="ml-auto shrink-0 font-mono text-theme-text-tertiary">
                {binding.rulesCount} rule{binding.rulesCount === 1 ? "" : "s"}
              </span>
              {binding.inheritedFromGroup ? (
                <Badge tone="note" size="sm">
                  via {binding.inheritedFromGroup}
                </Badge>
              ) : null}
            </div>
          ))}
        </div>
      ) : (
        <p className="text-xs italic text-theme-text-tertiary">
          No RoleBinding or ClusterRoleBinding grants this subject anything.
        </p>
      )}
      {usedByPods.length > 0 ? (
        <p className="text-xs leading-relaxed text-theme-text-secondary [overflow-wrap:anywhere]">
          <span className="text-theme-text-tertiary">Used by pods: </span>
          <span className="font-mono">{usedByPods.join(", ")}</span>
          {data.podsTotal && data.podsTotal > usedByPods.length
            ? ` and ${data.podsTotal - usedByPods.length} more`
            : ""}
        </p>
      ) : null}
    </div>
  );
}
