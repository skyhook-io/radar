import { Badge } from "../../ui/Badge";
import { Tooltip } from "../../ui/Tooltip";
import {
  getCalicoPolicyNamespaceSelector,
  getCalicoPolicyRuleCount,
  getCalicoPolicySelector,
  getCalicoPolicyServiceAccountSelector,
  getCalicoPolicyTypes,
  isCalicoPolicyResource,
} from "../resource-utils-calico";

export function CalicoPolicyCell({
  resource,
  column,
}: {
  resource: any;
  column: string;
}) {
  if (!isCalicoPolicyResource(resource))
    return <span className="text-sm text-theme-text-tertiary">-</span>;

  switch (column) {
    case "selector":
      return <SelectorCell value={getCalicoPolicySelector(resource)} />;
    case "namespaceSelector":
      return (
        <SelectorCell value={getCalicoPolicyNamespaceSelector(resource)} />
      );
    case "serviceAccountSelector":
      return (
        <SelectorCell value={getCalicoPolicyServiceAccountSelector(resource)} />
      );
    case "tier":
      return (
        <span className="text-sm text-theme-text-secondary">
          {resource.spec?.tier || "default"}
        </span>
      );
    case "order":
      return (
        <span className="text-sm text-theme-text-secondary">
          {resource.spec?.order ?? "-"}
        </span>
      );
    case "types":
      return (
        <div className="flex flex-wrap gap-1">
          {getCalicoPolicyTypes(resource).map((type) => (
            <Badge key={type} tone="accent1" size="sm">
              {type}
            </Badge>
          ))}
        </div>
      );
    case "stagedAction":
      return resource.spec?.stagedAction ? (
        <Badge tone="note" size="sm">
          {String(resource.spec.stagedAction)}
        </Badge>
      ) : (
        <span className="text-sm text-theme-text-tertiary">-</span>
      );
    case "rules": {
      const { ingress, egress } = getCalicoPolicyRuleCount(resource);
      return (
        <span className="text-sm text-theme-text-secondary">
          {ingress}i / {egress}e
        </span>
      );
    }
    default:
      return <span className="text-sm text-theme-text-tertiary">-</span>;
  }
}

function SelectorCell({ value }: { value: string }) {
  return (
    <Tooltip content={value}>
      <span className="text-sm text-theme-text-secondary truncate block font-mono">
        {value}
      </span>
    </Tooltip>
  );
}
