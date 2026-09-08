import type {
  InvestigationEventEvidence,
  InvestigationEvidenceSource,
} from "../types";
import {
  type ProjectionBuilder,
  event,
  invalidPayload,
  nonEmptyString,
  record,
  scopeFromArgs,
  sourceArgsRelevance,
  stringArray,
} from "../builder";
import { addEvents, addNarrowHint } from "../observations";

export function adaptEvents(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  payload: unknown,
): void {
  const value = record(payload);
  // The producer serializes an empty result as a nil slice, which is null.
  const eventsRaw = value?.events === null ? [] : value?.events;
  if (!value || !Array.isArray(eventsRaw)) {
    invalidPayload(builder, source);
    return;
  }
  const events = eventsRaw
    .map(event)
    .filter((item): item is InvestigationEventEvidence => Boolean(item));
  if (events.length !== eventsRaw.length) {
    invalidPayload(builder, source);
    return;
  }
  addNarrowHint(builder, source, value);
  // The producer answers a namespace the caller cannot read with an empty
  // list and marks it. Only that case is a coverage gap. Any other complete,
  // successful empty read answers the question it asked and is filed as a
  // checked receipt; a gap there would contradict an events card from another
  // call in the same turn. The receipt names the one remaining ambiguity for
  // producers that predate the marker.
  if (value.accessDenied === true) {
    builder.limit(
      source,
      "Events",
      `Events in ${scopeFromArgs(source)} are not readable with your permissions.`,
      "error",
    );
    return;
  }
  // A cluster-wide read the producer narrowed to the caller's namespaces
  // answers for those alone, so neither its empty receipt nor its card may
  // stand for the cluster.
  const narrowedTo = narrowedEventScope(value);
  if (narrowedTo && events.length > 0) {
    builder.limit(
      source,
      "Events",
      `This cluster-wide events read covered only the namespaces you can read (${narrowedTo}).`,
      "unknown",
    );
  }
  addEvents(
    builder,
    source,
    events,
    `events:${source.args ?? scopeFromArgs(source)}`,
    !nonEmptyString(value.narrowHint),
    true,
    sourceArgsRelevance(builder, source),
    narrowedTo
      ? {
          title: "No events in the namespaces you can read",
          message: `The events query completed and returned nothing for ${narrowedTo}. Namespaces outside your permissions were not read, so this does not clear the cluster.`,
        }
      : {
          title: "No events in this window",
          message:
            "The events query completed and returned nothing for this scope. Events outside its window or filters are not covered; a namespace you cannot read also returns nothing.",
        },
  );
}

/**
 * Names the namespaces a cluster-wide events read was actually narrowed to,
 * or undefined when the read covered everything the query asked for.
 */
function narrowedEventScope(
  value: Record<string, unknown>,
): string | undefined {
  if (value.partialScope !== true) return undefined;
  const namespaces = (stringArray(value.scopeNamespaces) ?? []).filter(
    nonEmptyString,
  );
  return namespaces.length > 0
    ? namespaces.join(", ")
    : "the namespaces you can read";
}
