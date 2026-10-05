import { DatumSummary } from "@skyhook-io/k8s-ui/components/datum/DatumSummary";
import { isDatumSummary } from "@skyhook-io/k8s-ui/components/datum/workspace";
import {
  refToSelectedResource,
  type NavigateToResource,
  PaneLoader,
  OpenIssueContext,
} from "@skyhook-io/k8s-ui";
import { useDatumWorkspace } from "../../api/datum";
import { DatumProjectConnection } from "./DatumProjectConnection";
import { useWorkspaceNavigate } from "../workspace/useWorkspaceNavigate";
import { issuesPathForSubject } from "../../utils/page-links";
export function DatumSummaryHost({
  resource,
  onNavigate,
}: {
  resource: any;
  onNavigate?: NavigateToResource;
}) {
  const { data, isLoading, error } = useDatumWorkspace(
    resource.metadata?.namespace ? [resource.metadata.namespace] : [],
  );
  const navigate = useWorkspaceNavigate();
  return (
    <OpenIssueContext.Provider
      value={(p) => navigate(issuesPathForSubject(p.subject))}
    >
      {resource.kind === "Project" && (
        <div className="px-4 pt-4">
          <DatumProjectConnection project={resource} />
        </div>
      )}
      {isLoading && (
        <PaneLoader label="Loading related Datum inventory…" className="h-24" />
      )}
      {error && (
        <p className="p-4 text-sm text-theme-text-secondary">
          Related Datum inventory could not be read: {error.message}
        </p>
      )}
      <DatumSummary
        object={resource}
        workspace={data}
        onNavigate={
          onNavigate
            ? (ref) => onNavigate(refToSelectedResource(ref))
            : undefined
        }
      />
    </OpenIssueContext.Provider>
  );
}
export function renderDatumSummary(
  resource: any,
  onNavigate?: NavigateToResource,
) {
  return isDatumSummary(resource) ? (
    <DatumSummaryHost resource={resource} onNavigate={onNavigate} />
  ) : null;
}
