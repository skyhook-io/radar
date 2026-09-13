import { useState } from "react";
import { Copy } from "lucide-react";
import type { Relationships, ResourceRef } from "../../../types";
import { Badge } from "../../ui/Badge";
import {
  AlertBanner,
  Property,
  PropertyList,
  ResourceLink,
  Section,
} from "../../ui/drawer-components";

const prefix = "reflector.v1.k8s.emberstack.com/";
const annotationNames = [
  "reflects",
  "auto-reflects",
  "reflected-version",
  "reflected-at",
  "reflection-allowed",
  "reflection-auto-enabled",
  "reflection-allowed-namespaces",
  "reflection-allowed-namespaces-selector",
  "reflection-auto-namespaces",
  "reflection-auto-namespaces-selector",
];

type Reflection = NonNullable<Relationships["reflection"]>;
interface ReflectorResource {
  apiVersion?: string;
  metadata?: {
    name?: string;
    namespace?: string;
    annotations?: Record<string, string>;
  };
}
interface ReflectorSectionProps {
  data: ReflectorResource;
  reflection?: Reflection;
  onNavigate?: (ref: ResourceRef) => void;
}

function annotationsFor(data: ReflectorResource): Record<string, string> {
  return data.apiVersion === "v1" ? (data.metadata?.annotations ?? {}) : {};
}

export function hasReflectorDetails(
  data: ReflectorResource,
  reflection?: Reflection,
): boolean {
  if (data.apiVersion !== "v1") return false;
  const annotations = annotationsFor(data);
  return (
    annotationNames.some((name) => prefix + name in annotations) ||
    !!reflection?.source ||
    !!reflection?.mirrors?.length
  );
}

export function isReflectorMirror(data: ReflectorResource): boolean {
  return /^[^/\s]+\/[^/\s]+$/.test(
    annotationsFor(data)[prefix + "reflects"] ?? "",
  );
}

export const reflectorEditNotice =
  "This object is a Reflector mirror. Edit the source for lasting changes. Local edits may persist until the source changes, then be overwritten.";

function booleanValue(value: string | undefined): boolean | undefined {
  if (value === undefined) return false;
  if (value.trim().toLowerCase() === "true") return true;
  if (value.trim().toLowerCase() === "false") return false;
  return undefined;
}

function NamespaceRule({
  patterns,
  selector,
}: {
  patterns?: string;
  selector?: string;
}) {
  if (!patterns && !selector) return <>No namespace restriction configured</>;
  return (
    <span className="space-y-1 block">
      {patterns && (
        <span className="block break-all">
          <span className="text-theme-text-tertiary">Name patterns: </span>
          {patterns}
        </span>
      )}
      {patterns && selector && (
        <span className="block text-theme-text-tertiary">or</span>
      )}
      {selector && (
        <span className="block break-all">
          <span className="text-theme-text-tertiary">Label selector: </span>
          {selector}
        </span>
      )}
    </span>
  );
}

export function ReflectorSection({
  data,
  reflection,
  onNavigate,
}: ReflectorSectionProps) {
  const [showAll, setShowAll] = useState(false);
  if (!hasReflectorDetails(data, reflection)) return null;
  const annotations = annotationsFor(data);
  const value = (name: string) => annotations[prefix + name];
  const declaration = value("reflects");
  const hasDeclaration = declaration !== undefined;
  const isMirror = isReflectorMirror(data);
  const allowed = booleanValue(value("reflection-allowed"));
  const autoEnabled = booleanValue(value("reflection-auto-enabled"));
  const automatic = booleanValue(value("auto-reflects"));
  const source =
    reflection?.source &&
    `${reflection.source.namespace}/${reflection.source.name}` === declaration
      ? reflection.source
      : undefined;
  const mirrors = reflection?.mirrors ?? [];
  const sourceSettings =
    !isMirror ||
    annotationNames.some(
      (name) => name.startsWith("reflection-") && value(name) !== undefined,
    ) ||
    mirrors.length > 0;
  const copiedVersion = value("reflected-version");
  const sourceVersion = source ? reflection?.sourceResourceVersion : undefined;
  const warnings: string[] = [];
  for (const name of [
    "reflection-allowed",
    "reflection-auto-enabled",
    "auto-reflects",
  ]) {
    if (booleanValue(value(name)) === undefined)
      warnings.push(
        `${name} must be true or false (currently "${value(name)}").`,
      );
  }
  if (hasDeclaration && !isMirror)
    warnings.push(
      "The reflects annotation must name a source as namespace/name.",
    );
  if (
    declaration &&
    declaration === `${data.metadata?.namespace}/${data.metadata?.name}`
  )
    warnings.push(
      "The reflects annotation points to this object itself. Choose a different source.",
    );
  if (!isMirror && mirrors.length > 0 && allowed === false)
    warnings.push(
      "Reflection is disabled on this source, but visible mirrors still reference it.",
    );
  if (!isMirror && autoEnabled === true && allowed === false)
    warnings.push(
      "Automatic mirror creation requires reflection-allowed to be true.",
    );
  if (
    isMirror &&
    (mirrors.length > 0 || allowed === true || autoEnabled === true)
  )
    warnings.push(
      "This object is itself a mirror. Reflector does not propagate its updates as source updates to other mirrors.",
    );

  return (
    <Section title="Reflector" icon={Copy} defaultExpanded>
      <div className="space-y-3">
        {warnings.length > 0 && (
          <AlertBanner
            variant="warning"
            title="Check reflection configuration"
            message={
              <ul className="list-disc pl-4 space-y-1">
                {warnings.map((warning) => (
                  <li key={warning}>{warning}</li>
                ))}
              </ul>
            }
          />
        )}
        <PropertyList>
          <Property
            label="Role"
            value={
              <Badge tone="structural">
                {isMirror
                  ? automatic === true
                    ? "Automatic mirror"
                    : automatic === false
                      ? "Manual mirror"
                      : "Mirror"
                  : hasDeclaration
                    ? "Invalid source reference"
                    : "Source"}
              </Badge>
            }
          />
          {hasDeclaration && (
            <Property
              label="Declared source"
              value={
                <span className="break-all">
                  {source ? (
                    <ResourceLink
                      {...source}
                      label={`${source.namespace}/${source.name}`}
                      onNavigate={onNavigate}
                    />
                  ) : (
                    declaration || "(empty)"
                  )}
                </span>
              }
            />
          )}
          {isMirror && !source && (
            <p className="text-xs text-theme-text-tertiary">
              Source is not available in this view; its existence and version
              are unverified.
            </p>
          )}
          {sourceSettings && (
            <>
              <Property
                label="Reflection allowed"
                value={
                  allowed === undefined
                    ? "Invalid annotation"
                    : allowed
                      ? "Yes"
                      : "No"
                }
              />
              <Property
                label="Allowed namespaces"
                value={
                  <NamespaceRule
                    patterns={value("reflection-allowed-namespaces")}
                    selector={value("reflection-allowed-namespaces-selector")}
                  />
                }
              />
              <Property
                label="Automatic creation"
                value={
                  autoEnabled === undefined
                    ? "Invalid annotation"
                    : autoEnabled
                      ? "Enabled"
                      : "Disabled"
                }
              />
              {(autoEnabled ||
                value("reflection-auto-namespaces") ||
                value("reflection-auto-namespaces-selector")) && (
                <Property
                  label="Automatic namespaces"
                  value={
                    <NamespaceRule
                      patterns={value("reflection-auto-namespaces")}
                      selector={value("reflection-auto-namespaces-selector")}
                    />
                  }
                />
              )}
              <p className="text-xs text-theme-text-tertiary">
                Automatic destinations must also satisfy the allowed rule.
                Controller version and settings may further limit destinations.
              </p>
            </>
          )}
          {isMirror && (
            <>
              <Property
                label="Copied source version"
                value={copiedVersion || "No version recorded"}
              />
              <Property
                label="Source version (snapshot)"
                value={sourceVersion}
              />
              <Property
                label="Recorded copy time"
                value={value("reflected-at")}
              />
              {copiedVersion && sourceVersion && (
                <p className="text-xs text-theme-text-secondary">
                  {copiedVersion === sourceVersion
                    ? "The recorded version matches the source snapshot."
                    : "The recorded version differs from the source snapshot."}{" "}
                  Cached versions can lag reconciliation; matching versions do
                  not prove matching contents.
                </p>
              )}
            </>
          )}
        </PropertyList>
        {mirrors.length > 0 && (
          <div className="space-y-2">
            <div className="text-sm text-theme-text-secondary">
              Visible mirrors{" "}
              <Badge tone="structural" size="sm">
                {mirrors.length}
              </Badge>
            </div>
            <div className="space-y-1 max-h-60 overflow-y-auto text-sm">
              {(showAll ? mirrors : mirrors.slice(0, 10)).map((ref) => (
                <div key={`${ref.namespace}/${ref.name}`} className="break-all">
                  <ResourceLink
                    {...ref}
                    label={`${ref.namespace}/${ref.name}`}
                    onNavigate={onNavigate}
                  />
                </div>
              ))}
            </div>
            {mirrors.length > 10 && (
              <button
                className="text-xs text-accent-text hover:underline"
                onClick={() => setShowAll(!showAll)}
              >
                {showAll
                  ? "Show fewer"
                  : `Show all ${mirrors.length} visible mirrors`}
              </button>
            )}
            <p className="text-xs text-theme-text-tertiary">
              Visible relationships only; other mirrors may exist.
            </p>
          </div>
        )}
        {sourceSettings && mirrors.length === 0 && (
          <p className="text-xs text-theme-text-tertiary">
            No mirrors visible in this view.
          </p>
        )}
        {isMirror && (
          <p className="text-xs text-theme-text-secondary">
            {reflectorEditNotice}
          </p>
        )}
      </div>
    </Section>
  );
}
