import { Info } from "lucide-react";
import {
  ResourceIssuesSection,
  type IssueResourceRef,
  type RelatedFindingsGroup,
} from "@skyhook-io/k8s-ui";
import { relatedFindingLimitations } from "../../utils/related-findings";

export function RelatedFindingsSection({
  data,
  onResourceClick,
}: {
  data: RelatedFindingsGroup[] | undefined;
  onResourceClick?: (ref: IssueResourceRef) => void;
}) {
  const groups = data?.filter(
    (group) =>
      group.findings?.length ||
      group.truncated ||
      group.unknown > 0 ||
      group.gaps.length > 0,
  );
  if (!groups?.length) return null;

  return (
    <div className="space-y-4">
      {groups.map((group, index) => {
        const anchor = group.associatedVia;
        const limits = relatedFindingLimitations(group);
        return (
          <div
            key={`${anchor.group}/${anchor.kind}/${anchor.namespace}/${anchor.name}/${index}`}
            className="space-y-2 border-b border-theme-border pb-4 last:border-0 last:pb-0"
          >
            {group.findings?.length ? (
              <ResourceIssuesSection
                heading="Related findings"
                issues={group.findings}
                onResourceClick={onResourceClick}
              />
            ) : (
              <h3 className="flex items-center gap-2 px-1 text-xs font-semibold uppercase tracking-wide text-theme-text-secondary">
                <Info
                  className="h-4 w-4 text-theme-text-tertiary"
                  aria-hidden
                />
                Related findings
              </h3>
            )}
            <p className="text-xs text-theme-text-secondary">
              Via{" "}
              {onResourceClick ? (
                <button
                  type="button"
                  className="text-accent hover:underline"
                  onClick={() => onResourceClick(anchor)}
                >
                  {`${anchor.kind} ${anchor.namespace ? `${anchor.namespace}/` : ""}${anchor.name}${anchor.group ? ` (${anchor.group})` : ""}`}
                </button>
              ) : (
                `${anchor.kind} ${anchor.namespace ? `${anchor.namespace}/` : ""}${anchor.name}${anchor.group ? ` (${anchor.group})` : ""}`
              )}
              . These findings concern the linked resources; this resource's
              status is reported separately.
            </p>
            {limits.length > 0 && (
              <ul className="text-xs text-theme-text-secondary">
                {limits.map((limit) => (
                  <li key={`${limit.kind}/${limit.message}`}>
                    {limit.message}
                  </li>
                ))}
              </ul>
            )}
          </div>
        );
      })}
    </div>
  );
}
