import type { IssueRecentChange } from "@skyhook-io/k8s-ui";
import type { InvestigationEvidenceSource } from "../types";
import {
  type ProjectionBuilder,
  invalidPayload,
  nonEmptyString,
  parseJSON,
  recentChange,
  record,
  scopeFromArgs,
  sourceArgsRelevance,
} from "../builder";
import { addChanges, addNarrowHint } from "../observations";

function changesSubjectFromArgs(
  source: InvestigationEvidenceSource,
): { kind?: string; namespace?: string; name: string } | undefined {
  const args = record(source.args ? parseJSON(source.args) : undefined);
  if (!nonEmptyString(args?.name)) return undefined;
  return {
    ...(nonEmptyString(args.kind) ? { kind: args.kind } : {}),
    ...(nonEmptyString(args.namespace) ? { namespace: args.namespace } : {}),
    name: args.name,
  };
}

export function adaptChanges(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  payload: unknown,
): void {
  const value = record(payload);
  // The producer serializes an empty result as a nil slice, which is null.
  const changesRaw = value?.changes === null ? [] : value?.changes;
  if (!value || !Array.isArray(changesRaw)) {
    invalidPayload(builder, source, "Recent changes");
    return;
  }
  const changes = changesRaw
    .map(recentChange)
    .filter((item): item is IssueRecentChange => Boolean(item));
  if (changes.length !== changesRaw.length) {
    invalidPayload(builder, source, "Recent changes");
    return;
  }
  addNarrowHint(builder, source, value);
  let sourceErrors = 0;
  if (Array.isArray(value.sourcesErrored)) {
    for (const sourceError of value.sourcesErrored) {
      if (nonEmptyString(sourceError)) {
        sourceErrors += 1;
        builder.limit(source, "Recent changes", sourceError, "error");
      }
    }
  } else if (value.sourcesErrored !== undefined) {
    invalidPayload(builder, source, "Recent changes source coverage");
  }
  // Reads of one resource's history are revisions of one card however the
  // window or cap differs; the window is stated in the title instead.
  const args = record(source.args ? parseJSON(source.args) : undefined);
  addChanges(
    builder,
    source,
    changes,
    `changes:${scopeFromArgs(source)}`,
    undefined,
    !nonEmptyString(value.narrowHint) && sourceErrors === 0,
    false,
    sourceArgsRelevance(builder, source),
    changesSubjectFromArgs(source),
    nonEmptyString(args?.since) ? args.since : undefined,
  );
}
