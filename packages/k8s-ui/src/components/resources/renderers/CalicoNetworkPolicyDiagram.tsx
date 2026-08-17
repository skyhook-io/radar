import { useId } from "react";
import { clsx } from "clsx";
import { Badge, type BadgeSeverity } from "../../ui/Badge";
import { Tooltip } from "../../ui/Tooltip";
import {
  formatKubernetesLabelSelector,
  getCalicoPolicyTypes,
} from "../resource-utils-calico";
import {
  NETWORK_POLICY_PEER_DOTS,
  NETWORK_POLICY_PEER_STYLES,
  type NetworkPolicyPeerType,
} from "./network-policy-peer-styles";

interface CalicoNetworkPolicyDiagramProps {
  spec: any;
  staged?: boolean;
}

type Direction = "ingress" | "egress";

interface EntityField {
  label: string;
  values: string[];
  negative?: boolean;
}

const ACTION_SEVERITY: Record<string, BadgeSeverity> = {
  allow: "success",
  deny: "error",
  log: "warning",
  pass: "info",
};

export function CalicoNetworkPolicyDiagram({
  spec,
  staged = false,
}: CalicoNetworkPolicyDiagramProps) {
  const policyTypes = getCalicoPolicyTypes({ spec }).map((type) =>
    type.toLowerCase(),
  );
  const directions = [
    {
      direction: "ingress" as const,
      label: "Ingress",
      enabled:
        policyTypes.length > 0
          ? policyTypes.includes("ingress")
          : Array.isArray(spec?.ingress),
    },
    {
      direction: "egress" as const,
      label: "Egress",
      enabled:
        policyTypes.length > 0
          ? policyTypes.includes("egress")
          : Array.isArray(spec?.egress),
    },
  ].filter((entry) => entry.enabled);
  const target = {
    selector: spec?.selector,
    namespaceSelector: spec?.namespaceSelector,
    serviceAccountSelector: spec?.serviceAccountSelector,
  };

  return (
    <div className="card-inner-lg space-y-3">
      {staged && (
        <div className="flex items-center gap-2 text-[11px] text-theme-text-tertiary">
          <Badge severity="warning" size="sm">
            Staged preview
          </Badge>
          <span>Dashed paths are evaluated but not enforced</span>
        </div>
      )}

      {directions.map(({ direction, label }) => (
        <DirectionSection
          key={direction}
          direction={direction}
          label={label}
          rules={Array.isArray(spec?.[direction]) ? spec[direction] : []}
          target={target}
          staged={staged}
        />
      ))}

      {directions.length === 0 && (
        <div className="py-2 text-center text-xs text-theme-text-tertiary">
          No explicit ingress or egress rules
        </div>
      )}
    </div>
  );
}

function DirectionSection({
  direction,
  label,
  rules,
  target,
  staged,
}: {
  direction: Direction;
  label: string;
  rules: any[];
  target: any;
  staged: boolean;
}) {
  return (
    <div className="space-y-2">
      <div className="flex items-center gap-1.5">
        <span
          className={clsx(
            "h-1.5 w-1.5 rounded-full",
            direction === "ingress" ? "bg-blue-500" : "bg-purple-500",
          )}
        />
        <span
          className={clsx(
            "text-[10px] font-semibold uppercase tracking-wider",
            direction === "ingress" ? "text-blue-400" : "text-purple-400",
          )}
        >
          {label}
        </span>
      </div>

      {rules.length === 0 ? (
        <div className="rounded-md border border-theme-border-subtle px-2 py-2 text-xs text-theme-text-tertiary">
          No explicit {direction} rules
        </div>
      ) : (
        <div className="divide-y divide-theme-border-subtle rounded-md border border-theme-border-subtle bg-theme-base/40">
          {rules.map((rule, index) => (
            <FlowRow
              key={index}
              direction={direction}
              rule={rule}
              target={target}
              staged={staged}
            />
          ))}
        </div>
      )}
    </div>
  );
}

function FlowRow({
  direction,
  rule,
  target,
  staged,
}: {
  direction: Direction;
  rule: any;
  target: any;
  staged: boolean;
}) {
  const action = String(rule?.action ?? "Allow");
  const protocol = rule?.protocol;
  const notProtocol = rule?.notProtocol;
  const isIngress = direction === "ingress";
  const constraints = pathConstraints(rule);
  const flowConstraints =
    constraints.length > 0
      ? constraints
      : protocol !== undefined
        ? [`${formatProtocol(protocol).toUpperCase()}/*`]
        : [];
  const leftEntities = isIngress
    ? [rule?.source]
    : [target, rule?.source];
  const rightEntities = isIngress
    ? [target, rule?.destination]
    : [rule?.destination];

  return (
    <div className="space-y-2 px-2 py-2">
      <div className="flex flex-wrap items-center gap-1.5">
        <Badge
          severity={ACTION_SEVERITY[action.toLowerCase()] ?? "neutral"}
          size="sm"
        >
          {action}
        </Badge>
        {notProtocol !== undefined && (
          <Badge tone="note" size="sm">
            not {formatProtocol(notProtocol)}
          </Badge>
        )}
      </div>

      <div className="grid grid-cols-[minmax(0,1fr)_3.5rem_minmax(0,1fr)] items-center gap-1">
        <Endpoint
          entities={leftEntities}
          emptyText={isIngress ? "Any source" : "All workloads"}
          target={!isIngress}
        />
        <FlowArrow
          action={action}
          direction={direction}
          staged={staged}
          constraints={flowConstraints}
        />
        <Endpoint
          entities={rightEntities}
          emptyText={isIngress ? "All workloads" : "Any destination"}
          target={isIngress}
        />
      </div>
    </div>
  );
}

function FlowArrow({
  action,
  direction,
  staged,
  constraints,
}: {
  action: string;
  direction: Direction;
  staged: boolean;
  constraints: string[];
}) {
  const markerId = useId().replace(/:/g, "");
  const color = arrowColor(action, direction);
  const dashed = staged || action.toLowerCase() !== "allow";

  return (
    <div className="flex min-w-0 flex-col items-center justify-center">
      <svg
        width="48"
        height="18"
        viewBox="0 0 48 18"
        className="overflow-visible"
        aria-hidden="true"
      >
        <defs>
          <marker
            id={markerId}
            markerWidth="6"
            markerHeight="6"
            refX="5"
            refY="3"
            orient="auto"
          >
            <path
              d="M0,0 L6,3 L0,6"
              fill="none"
              stroke={color}
              strokeWidth="1.5"
            />
          </marker>
        </defs>
        <line
          x1="2"
          y1="9"
          x2="40"
          y2="9"
          stroke={color}
          strokeWidth="1.5"
          strokeDasharray={dashed ? "4 3" : undefined}
          markerEnd={`url(#${markerId})`}
        />
      </svg>
      {constraints.length > 0 && (
        <Tooltip content={constraints.join("\n")} position="top">
          <span className="flex flex-col items-center gap-0.5 font-mono text-[8px] leading-none text-theme-text-tertiary">
            {constraints.map((constraint, index) => (
              <span key={`${constraint}-${index}`} className="whitespace-nowrap">
                {constraint}
              </span>
            ))}
          </span>
        </Tooltip>
      )}
    </div>
  );
}

function Endpoint({
  entities,
  emptyText,
  target = false,
}: {
  entities: any[];
  emptyText: string;
  target?: boolean;
}) {
  const fields = entities.flatMap(entityFields);
  const peerType = classifyPeer(fields);
  const tooltip = fields.length
    ? fields
        .map((field) => `${field.label}: ${field.values.join(", ")}`)
        .join("\n")
    : emptyText;

  return (
    <Tooltip content={tooltip} position="top">
      <div
          className={clsx(
            "min-w-0 overflow-hidden rounded-md border px-2 py-1.5",
            target
              ? "border-indigo-500/30 bg-indigo-500/8"
              : NETWORK_POLICY_PEER_STYLES[peerType],
        )}
      >
        <div className="flex min-w-0 items-start gap-1.5">
          <span
            className={clsx(
              "mt-1 h-1.5 w-1.5 shrink-0 rounded-full",
              target ? "bg-indigo-500" : NETWORK_POLICY_PEER_DOTS[peerType],
            )}
          />
          <div className="min-w-0 flex-1">
            {fields.length > 0 ? (
              <EntityFields fields={fields} />
            ) : (
              <div className="truncate text-[11px] font-medium">{emptyText}</div>
            )}
          </div>
        </div>
      </div>
    </Tooltip>
  );
}

function EntityFields({ fields }: { fields: EntityField[] }) {
  return (
    <div className="min-w-0 space-y-0.5">
      {fields.map((field, index) => (
        <div
          key={`${field.label}-${index}`}
          className="flex min-w-0 items-baseline gap-1 text-[9px]"
        >
          <span
            className={clsx(
              "shrink-0 text-theme-text-tertiary",
              field.negative && "text-warning-text",
            )}
          >
            {field.label}
          </span>
          <span
            className={clsx(
              "min-w-0 truncate font-mono text-[10px] text-theme-text-primary",
              field.negative && "line-through",
            )}
          >
            {field.values.join(", ")}
          </span>
        </div>
      ))}
    </div>
  );
}

function classifyPeer(fields: EntityField[]): NetworkPolicyPeerType {
  const labels = new Set(fields.map((field) => field.label));
  const hasPodMatch = [
    "Selector",
    "Pod selector",
    "Not selector",
    "Service account",
    "Service accounts",
    "Not service accounts",
  ].some((label) => labels.has(label));
  const hasNamespaceMatch = ["Namespace", "Not namespace"].some((label) =>
    labels.has(label),
  );
  const hasNetworkMatch = ["Nets", "Not Nets", "IP block"].some((label) =>
    labels.has(label),
  );

  if (hasPodMatch && hasNamespaceMatch) return "combined";
  if (hasPodMatch) return "pod";
  if (hasNamespaceMatch) return "namespace";
  if (hasNetworkMatch) return "cidr";
  return "all";
}

function entityFields(entity: any): EntityField[] {
  if (!entity || typeof entity !== "object") return [];

  const fields: EntityField[] = [];
  addField(fields, "Selector", entity.selector);
  addField(
    fields,
    "Pod selector",
    entity.podSelector === undefined
      ? undefined
      : formatKubernetesLabelSelector(entity.podSelector),
  );
  addField(fields, "Not selector", entity.notSelector, true);
  addField(
    fields,
    "Namespace",
    entity.namespaceSelector && typeof entity.namespaceSelector === "object"
      ? formatKubernetesLabelSelector(entity.namespaceSelector)
      : entity.namespaceSelector,
  );
  addField(fields, "Not namespace", entity.notNamespaceSelector, true);
  addField(fields, "Service account", entity.serviceAccountSelector);
  addField(fields, "Nets", entity.nets);
  addField(fields, "IP block", entity.ipBlock);
  addField(fields, "Not Nets", entity.notNets, true);
  addField(
    fields,
    "Service accounts",
    serviceAccountValues(entity.serviceAccounts),
  );
  addField(
    fields,
    "Not service accounts",
    serviceAccountValues(entity.notServiceAccounts),
    true,
  );
  return fields;
}

function addField(
  fields: EntityField[],
  label: string,
  value: any,
  negative = false,
) {
  const values = valuesFor(value)
    .map(formatValue)
    .filter((item) => item !== "");
  if (values.length === 0) return;
  fields.push({ label, values, negative });
}

function valuesFor(value: any): any[] {
  if (value === undefined || value === null || value === "") return [];
  return Array.isArray(value) ? value : [value];
}

function serviceAccountValues(value: any): string[] {
  if (!value || typeof value !== "object") return [];
  return [
    ...(Array.isArray(value.names) ? value.names.map(String) : []),
    ...(value.selector !== undefined
      ? [`selector: ${String(value.selector)}`]
      : []),
  ];
}

function pathConstraints(rule: any): string[] {
  const sourcePorts = valuesFor(rule?.source?.ports).map((port) =>
    formatPort(port, rule?.protocol),
  );
  const notSourcePorts = valuesFor(rule?.source?.notPorts).map((port) =>
    formatPort(port, rule?.protocol),
  );
  const destinationPorts = valuesFor(rule?.destination?.ports).map((port) =>
    formatPort(port, rule?.protocol),
  );
  const notDestinationPorts = valuesFor(rule?.destination?.notPorts).map((port) =>
    formatPort(port, rule?.protocol),
  );
  return [
    ...sourcePorts.map((port) => `src:${port}`),
    ...notSourcePorts.map((port) => `src:!${port}`),
    ...destinationPorts,
    ...notDestinationPorts.map((port) => `!${port}`),
  ];
}

function arrowColor(action: string, direction: Direction): string {
  switch (action.toLowerCase()) {
    case "deny":
      return "#ef4444";
    case "log":
      return "#f59e0b";
    case "pass":
      return "#3b82f6";
    default:
      return direction === "ingress" ? "#3b82f6" : "#a855f7";
  }
}

function formatValue(value: any): string {
  if (value && typeof value === "object") {
    if ("port" in value) {
      const protocol = value.protocol ? `${value.protocol}/` : "";
      const endPort = value.endPort !== undefined ? `-${value.endPort}` : "";
      return `${protocol}${value.port}${endPort}`;
    }
    if ("start" in value || "end" in value) {
      return `${value.start ?? ""}-${value.end ?? ""}`;
    }
    if ("strVal" in value) return String(value.strVal);
    if ("intVal" in value) return String(value.intVal);
    return JSON.stringify(value);
  }
  return String(value);
}

function formatPort(value: any, ruleProtocol?: any): string {
  if (value && typeof value === "object") {
    const protocol = value.protocol
      ? formatProtocol(value.protocol).toUpperCase()
      : ruleProtocol !== undefined
        ? formatProtocol(ruleProtocol).toUpperCase()
        : "TCP";
    if ("port" in value) {
      const endPort = value.endPort !== undefined ? `-${value.endPort}` : "";
      return `${protocol}/${value.port}${endPort}`;
    }
    if ("start" in value || "end" in value) {
      return `${protocol}/${value.start ?? ""}-${value.end ?? ""}`;
    }
    if ("strVal" in value) return `${protocol}/${value.strVal}`;
    if ("intVal" in value) return `${protocol}/${value.intVal}`;
  }
  const protocol =
    ruleProtocol !== undefined
      ? formatProtocol(ruleProtocol).toUpperCase()
      : "TCP";
  return `${protocol}/${formatValue(value)}`;
}

function formatProtocol(value: any): string {
  if (value && typeof value === "object") {
    if (value.strVal !== undefined) return String(value.strVal);
    if (value.intVal !== undefined) return String(value.intVal);
    return JSON.stringify(value);
  }
  return String(value);
}
