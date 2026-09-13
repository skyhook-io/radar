import { useState } from "react";
import { Copy } from "lucide-react";
import type { ResourceRef } from "../../../types";
import { pluralize } from "../../../utils/pluralize";
import { formatRelativeAgeTime } from "../../../utils/format";
import {
  hasReflectorDetails,
  parseReflector,
  type Reflection,
  type ReflectorResource,
} from "../../../utils/reflector";
export {
  hasReflectorDetails,
  isReflectorMirror,
  reflectorEditNotice,
} from "../../../utils/reflector";
import { Badge } from "../../ui/Badge";
import {
  AlertBanner,
  Property,
  PropertyList,
  ResourceLink,
  Section,
} from "../../ui/drawer-components";

interface ReflectorSectionProps {
  data: ReflectorResource;
  reflection?: Reflection;
  onNavigate?: (ref: ResourceRef) => void;
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
  const {
    value,
    hasDeclaration,
    isMirror,
    allowed,
    autoEnabled,
    automatic,
    mirrors,
    sourceSettings,
    copiedVersion,
    sourceVersion,
    warnings,
  } = parseReflector(data, reflection);

  return (
    <Section
      title={
        mirrors.length
          ? `Reflector (${pluralize(mirrors.length, "visible mirror")})`
          : "Reflector"
      }
      icon={Copy}
      defaultExpanded={warnings.length > 0}
    >
      <div className="space-y-3">
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
                value={
                  value("reflected-at") ? (
                    <span title={value("reflected-at")}>
                      {formatRelativeAgeTime(value("reflected-at"))}
                    </span>
                  ) : undefined
                }
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
              {(showAll ? mirrors : mirrors.slice(0, 5)).map((ref) => (
                <div key={`${ref.namespace}/${ref.name}`} className="break-all">
                  <ResourceLink
                    {...ref}
                    label={`${ref.namespace}/${ref.name}`}
                    onNavigate={onNavigate}
                  />
                </div>
              ))}
            </div>
            {mirrors.length > 5 && (
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
      </div>
    </Section>
  );
}

export function ReflectorSummary({
  data,
  reflection,
  onNavigate,
}: ReflectorSectionProps) {
  if (!hasReflectorDetails(data, reflection)) return null;
  const { isMirror, declaration, source, warnings } = parseReflector(
    data,
    reflection,
  );
  if (!isMirror && warnings.length === 0) return null;
  return (
    <div className="space-y-2">
      {isMirror && (
        <AlertBanner
          variant="info"
          title="Reflector mirror"
          message={
            <>
              <span className="block break-all">
                Source:{" "}
                {source ? (
                  <ResourceLink
                    {...source}
                    label={`${source.namespace}/${source.name}`}
                    onNavigate={onNavigate}
                  />
                ) : (
                  declaration
                )}
              </span>
              {!source && (
                <span className="block">
                  Source is not available in this view; its existence and
                  version are unverified.
                </span>
              )}
              <span className="block mt-1">
                Edit the source for lasting changes. Local edits may persist
                until the source changes, then be overwritten.
              </span>
            </>
          }
        />
      )}
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
    </div>
  );
}
