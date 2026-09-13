import type { Relationships } from "../types";

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

export type Reflection = NonNullable<Relationships["reflection"]>;
export interface ReflectorResource {
  apiVersion?: string;
  metadata?: {
    name?: string;
    namespace?: string;
    annotations?: Record<string, string>;
  };
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

export function parseReflector(
  data: ReflectorResource,
  reflection?: Reflection,
) {
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
      "Reflection is not enabled on this source, but visible mirrors still reference it.",
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

  return {
    value,
    declaration,
    hasDeclaration,
    isMirror,
    allowed,
    autoEnabled,
    automatic,
    source,
    mirrors,
    sourceSettings,
    copiedVersion,
    sourceVersion,
    warnings,
  };
}
