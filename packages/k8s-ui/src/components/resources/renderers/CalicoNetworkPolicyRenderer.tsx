import {
  ArrowDownToLine,
  ArrowUpFromLine,
  GitFork,
  ShieldCheck,
} from "lucide-react";
import { Badge, type BadgeSeverity } from "../../ui/Badge";
import {
  Property,
  PropertyList,
  ResourceRefBadge,
  Section,
} from "../../ui/drawer-components";
import type { ResourceRef } from "../../../types";
import {
  getCalicoTierRef,
  getCalicoPolicyKindLabel,
  getCalicoPolicySelector,
  getCalicoPolicyTypes,
  formatKubernetesLabelSelector,
} from "../resource-utils-calico";
import { CalicoNetworkPolicyDiagram } from "./CalicoNetworkPolicyDiagram";

interface CalicoNetworkPolicyRendererProps {
  data: any;
  onNavigate?: (ref: ResourceRef) => void;
}

const ACTION_SEVERITY: Record<string, BadgeSeverity> = {
  allow: "success",
  deny: "error",
  log: "warning",
  pass: "info",
};

export function CalicoNetworkPolicyRenderer({
  data,
  onNavigate,
}: CalicoNetworkPolicyRendererProps) {
  const spec = data?.spec ?? {};
  const kindLabel = getCalicoPolicyKindLabel(data?.kind);
  const staged = kindLabel.includes("Staged");
  const ingress = Array.isArray(spec.ingress) ? spec.ingress : undefined;
  const egress = Array.isArray(spec.egress) ? spec.egress : undefined;
  const types = getCalicoPolicyTypes(data);
  const tierRef = getCalicoTierRef(data);

  return (
    <>
      <Section title="Policy Flow" icon={GitFork} defaultExpanded>
        <CalicoNetworkPolicyDiagram spec={spec} staged={staged} />
      </Section>

      <Section title="Policy" icon={ShieldCheck}>
        <div className="space-y-3">
          <Badge kind={kindLabel}>{kindLabel}</Badge>
          <PropertyList>
            <Property
              label="Selector"
              value={
                <SelectorValue
                  value={getCalicoPolicySelector(data)}
                  emptyText="all workloads"
                />
              }
            />
            {spec.namespaceSelector !== undefined && (
              <Property
                label="Namespace Selector"
                value={
                  <SelectorValue
                    value={spec.namespaceSelector}
                    emptyText="all namespaces"
                  />
                }
              />
            )}
            {spec.serviceAccountSelector !== undefined && (
              <Property
                label="Service Account Selector"
                value={
                  <SelectorValue
                    value={spec.serviceAccountSelector}
                    emptyText="all service accounts"
                  />
                }
              />
            )}
            <Property
              label="Tier"
              value={
                <ResourceRefBadge resourceRef={tierRef} onClick={onNavigate} />
              }
            />
            {spec.order !== undefined && (
              <Property label="Order" value={spec.order} />
            )}
            {types.length > 0 && (
              <Property label="Types" value={<BadgeList values={types} />} />
            )}
            {staged && spec.stagedAction !== undefined && (
              <Property
                label="Staged Action"
                value={<Badge tone="note">{String(spec.stagedAction)}</Badge>}
              />
            )}
            {spec.preDNAT !== undefined && (
              <Property label="Pre-DNAT" value={yesNo(spec.preDNAT)} />
            )}
            {spec.applyOnForward !== undefined && (
              <Property
                label="Apply On Forward"
                value={yesNo(spec.applyOnForward)}
              />
            )}
            {spec.doNotTrack !== undefined && (
              <Property label="Do Not Track" value={yesNo(spec.doNotTrack)} />
            )}
            {Array.isArray(spec.performanceHints) &&
              spec.performanceHints.length > 0 && (
                <Property
                  label="Performance Hints"
                  value={
                    <BadgeList values={spec.performanceHints} tone="note" />
                  }
                />
              )}
          </PropertyList>
        </div>
      </Section>

      {ingress && (
        <RuleSection
          title={`Ingress Rules (${ingress.length})`}
          icon={ArrowDownToLine}
          rules={ingress}
          direction="ingress"
        />
      )}
      {egress && (
        <RuleSection
          title={`Egress Rules (${egress.length})`}
          icon={ArrowUpFromLine}
          rules={egress}
          direction="egress"
        />
      )}
    </>
  );
}

function RuleSection({
  title,
  icon: Icon,
  rules,
  direction,
}: {
  title: string;
  icon: React.ComponentType<{ className?: string }>;
  rules: any[];
  direction: "ingress" | "egress";
}) {
  return (
    <Section title={title} icon={Icon} defaultExpanded>
      {rules.length === 0 ? (
        <div className="text-sm text-theme-text-tertiary">No rules</div>
      ) : (
        <div className="space-y-3">
          {rules.map((rule, index) => (
            <CalicoRuleCard key={index} rule={rule} direction={direction} />
          ))}
        </div>
      )}
    </Section>
  );
}

function CalicoRuleCard({
  rule,
  direction,
}: {
  rule: any;
  direction: "ingress" | "egress";
}) {
  const action = String(rule?.action ?? "Allow");
  const protocol = rule?.protocol;
  const notProtocol = rule?.notProtocol;
  const hasPorts = hasCalicoPortConstraints(rule);
  const primaryEntity = direction === "ingress" ? rule?.source : rule?.destination;
  const secondaryEntity = direction === "ingress" ? rule?.destination : rule?.source;
  const primaryLabel = direction === "ingress" ? "Source" : "Destination";
  const secondaryLabel = direction === "ingress" ? "Destination" : "Source";

  return (
    <div className="card-inner-lg space-y-3">
      <div className="mb-2 flex flex-wrap items-center gap-2">
        <Badge severity={ACTION_SEVERITY[action.toLowerCase()] ?? "neutral"}>
          {action}
        </Badge>
        {protocol !== undefined && !hasPorts && (
          <Badge protocol={formatProtocol(protocol)} size="sm">
            {formatProtocol(protocol)}
          </Badge>
        )}
        {notProtocol !== undefined && (
          <Badge tone="note" size="sm">
            not {formatProtocol(notProtocol)}
          </Badge>
        )}
      </div>

      <EntityRuleBlock label={primaryLabel} entity={primaryEntity} />
      <EntityRuleBlock label={secondaryLabel} entity={secondaryEntity} />
      <PortBlock
        label="Source Ports"
        entity={rule?.source}
        ruleProtocol={protocol}
      />
      <PortBlock
        label="Ports"
        entity={rule?.destination}
        ruleProtocol={protocol}
      />

      {(rule?.http !== undefined ||
        rule?.icmp !== undefined ||
        rule?.notICMP !== undefined) && (
        <div className="space-y-2">
          {rule.http !== undefined && <HTTPMatch value={rule.http} />}
          {rule.icmp !== undefined && (
            <ICMPMatch label="ICMP" value={rule.icmp} />
          )}
          {rule.notICMP !== undefined && (
            <ICMPMatch label="Not ICMP" value={rule.notICMP} negative />
          )}
        </div>
      )}
    </div>
  );
}

interface EntityRuleField {
  label: string;
  value: React.ReactNode;
  negative?: boolean;
}

function EntityRuleBlock({ label, entity }: { label: string; entity: any }) {
  const peers = (Array.isArray(entity) ? entity : [entity])
    .map(entityFields)
    .filter((fields) => fields.length > 0);
  if (peers.length === 0) return null;

  return (
    <div className="mb-2">
      <div className="text-xs text-theme-text-tertiary mb-1">{label}</div>
      <div className="space-y-1.5">
        {peers.map((fields, peerIndex) => (
          <div key={peerIndex} className="space-y-0.5 text-sm">
            {fields.map((field, fieldIndex) => (
              <div
                key={`${field.label}-${fieldIndex}`}
                className="flex flex-wrap items-baseline gap-1"
              >
                <span
                  className={
                    field.negative
                      ? "text-warning-text text-xs shrink-0"
                      : "text-theme-text-secondary text-xs shrink-0"
                  }
                >
                  {field.label}:
                </span>
                <div className="flex flex-wrap gap-1 min-w-0">{field.value}</div>
              </div>
            ))}
          </div>
        ))}
      </div>
    </div>
  );
}

function entityFields(
  entity: any,
): EntityRuleField[] {
  if (!entity || typeof entity !== "object") return [];

  const fields: EntityRuleField[] = [];
  addEntityField(
    fields,
    "Pod Selector",
    entity.podSelector === undefined
      ? undefined
      : formatKubernetesLabelSelector(entity.podSelector),
  );
  addEntityField(fields, "Selector", entity.selector);
  addEntityField(fields, "Not Selector", entity.notSelector, true);
  addEntityField(
    fields,
    "Namespace Selector",
    entity.namespaceSelector && typeof entity.namespaceSelector === "object"
      ? formatKubernetesLabelSelector(entity.namespaceSelector)
      : entity.namespaceSelector,
  );
  addEntityField(fields, "IP Block", entity.ipBlock);
  addEntityField(
    fields,
    "Not Namespace Selector",
    entity.notNamespaceSelector,
    true,
  );
  addEntityField(fields, "Nets", entity.nets);
  addEntityField(fields, "Not Nets", entity.notNets, true);
  addEntityField(fields, "Service Accounts", entity.serviceAccounts);
  addEntityField(
    fields,
    "Not Service Accounts",
    entity.notServiceAccounts,
    true,
  );
  return fields;
}

function addEntityField(
  fields: EntityRuleField[],
  label: string,
  value: any,
  negative = false,
) {
  if (
    value === undefined ||
    value === null ||
    value === "" ||
    (Array.isArray(value) && value.length === 0)
  )
    return;
  fields.push({ label, value: renderEntityValue(value, negative), negative });
}

function PortBlock({
  label,
  entity,
  ruleProtocol,
}: {
  label: string;
  entity: any;
  ruleProtocol: any;
}) {
  const entries = [
    ...valuesFor(entity?.ports).map((port) => ({
      value: formatPort(port, ruleProtocol),
      negative: false,
    })),
    ...valuesFor(entity?.notPorts).map((port) => ({
      value: formatPort(port, ruleProtocol),
      negative: true,
    })),
  ];
  if (entries.length === 0) return null;

  return (
    <div className="mb-2">
      <div className="text-xs text-theme-text-tertiary mb-1">{label}</div>
      <div className="flex flex-wrap gap-1">
        {entries.map((entry, index) => (
          <Badge
            key={`${entry.value}-${index}`}
            tone={entry.negative ? "note" : "structural"}
            size="sm"
            className={
              entry.negative
                ? "font-mono whitespace-nowrap line-through"
                : "font-mono whitespace-nowrap"
            }
          >
            {entry.negative ? `not ${entry.value}` : entry.value}
          </Badge>
        ))}
      </div>
    </div>
  );
}

function hasCalicoPortConstraints(rule: any): boolean {
  return (
    valuesFor(rule?.source?.ports).length > 0 ||
    valuesFor(rule?.source?.notPorts).length > 0 ||
    valuesFor(rule?.destination?.ports).length > 0 ||
    valuesFor(rule?.destination?.notPorts).length > 0
  );
}

function valuesFor(value: any): any[] {
  if (value === undefined || value === null || value === "") return [];
  return Array.isArray(value) ? value : [value];
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

function renderEntityValue(value: any, negative = false): React.ReactNode {
  if (
    value &&
    typeof value === "object" &&
    !Array.isArray(value) &&
    ("selector" in value || "namespaces" in value || "names" in value)
  ) {
    const items: React.ReactNode[] = [];
    if (value.selector !== undefined)
      items.push(
        <SelectorValue
          key="selector"
          value={value.selector}
          negative={negative}
        />,
      );
    for (const name of value.names ?? value.namespaces ?? []) {
      items.push(
        <ValueBadge key={String(name)} value={name} negative={negative} />,
      );
    }
    return items.length > 0 ? (
      items
    ) : (
      <ValueBadge value={JSON.stringify(value)} negative={negative} />
    );
  }

  const values = Array.isArray(value) ? value : [value];
  return values.map((item, index) => (
    <ValueBadge
      key={`${String(item)}-${index}`}
      value={formatValue(item)}
      negative={negative}
    />
  ));
}

function HTTPMatch({ value }: { value: any }) {
  return (
    <div className="card-inner text-xs space-y-2">
      <div className="text-theme-text-tertiary">HTTP</div>
      {value?.methods?.length > 0 && (
        <InlineField label="Methods" values={value.methods} tone="accent1" />
      )}
      {value?.notMethods?.length > 0 && (
        <InlineField
          label="Not Methods"
          values={value.notMethods}
          tone="note"
        />
      )}
      {value?.paths?.length > 0 && (
        <InlineField label="Paths" values={value.paths} />
      )}
      {value?.notPaths?.length > 0 && (
        <InlineField label="Not Paths" values={value.notPaths} tone="note" />
      )}
      {!value?.methods?.length &&
        !value?.notMethods?.length &&
        !value?.paths?.length &&
        !value?.notPaths?.length && (
          <span className="text-theme-text-secondary">Any HTTP request</span>
        )}
    </div>
  );
}

function ICMPMatch({
  label,
  value,
  negative = false,
}: {
  label: string;
  value: any;
  negative?: boolean;
}) {
  const fields = Array.isArray(value) ? value : [value];
  return (
    <div className="card-inner text-xs">
      <div className="text-theme-text-tertiary mb-1">{label}</div>
      <div className="flex flex-wrap gap-1">
        {fields.map((field, index) => {
          const text =
            field && typeof field === "object"
              ? [
                  field.family !== undefined && `family ${field.family}`,
                  field.type !== undefined && `type ${field.type}`,
                  field.code !== undefined && `code ${field.code}`,
                ]
                  .filter(Boolean)
                  .join(", ") || JSON.stringify(field)
              : String(field);
          return (
            <ValueBadge
              key={`${text}-${index}`}
              value={text || "any"}
              negative={negative}
            />
          );
        })}
      </div>
    </div>
  );
}

function InlineField({
  label,
  values,
  tone,
}: {
  label: string;
  values: any[];
  tone?: "note" | "accent1" | "structural";
}) {
  return (
    <div className="flex flex-wrap items-center gap-1">
      <span className="text-theme-text-secondary mr-1">{label}:</span>
      {values.map((value, index) => (
        <ValueBadge
          key={`${String(value)}-${index}`}
          value={value}
          tone={tone}
        />
      ))}
    </div>
  );
}

function SelectorValue({
  value,
  emptyText,
  negative = false,
}: {
  value: any;
  emptyText?: string;
  negative?: boolean;
}) {
  if (value === undefined || value === null || value === "") {
    return (
      <span className="text-theme-text-tertiary">{emptyText ?? "Any"}</span>
    );
  }
  return <ValueBadge value={value} negative={negative} />;
}

function ValueBadge({
  value,
  negative = false,
  tone,
}: {
  value: any;
  negative?: boolean;
  tone?: "note" | "accent1" | "structural";
}) {
  return (
    <Badge
      tone={tone ?? (negative ? "note" : "structural")}
      size="sm"
      className="font-mono whitespace-normal break-all"
    >
      {String(value)}
    </Badge>
  );
}

function BadgeList({
  values,
  tone = "structural",
}: {
  values: any[];
  tone?: "note" | "accent1" | "structural";
}) {
  return (
    <span className="flex flex-wrap gap-1">
      {values.map((value, index) => (
        <Badge key={`${String(value)}-${index}`} tone={tone} size="sm">
          {String(value)}
        </Badge>
      ))}
    </span>
  );
}

function formatValue(value: any): string {
  if (value && typeof value === "object") {
    if ("port" in value) {
      const protocol = value.protocol ? `${value.protocol}/` : "";
      const end = value.endPort !== undefined ? `-${value.endPort}` : "";
      return `${protocol}${value.port}${end}`;
    }
    if ("start" in value || "end" in value)
      return `${value.start ?? ""}-${value.end ?? ""}`;
    if ("strVal" in value) return String(value.strVal);
    if ("intVal" in value) return String(value.intVal);
    return JSON.stringify(value);
  }
  return String(value);
}

function yesNo(value: any): string {
  return value ? "Yes" : "No";
}

function formatProtocol(value: any): string {
  if (value && typeof value === "object") {
    if (value.strVal !== undefined) return String(value.strVal);
    if (value.intVal !== undefined) return String(value.intVal);
    return JSON.stringify(value);
  }
  return String(value);
}
