// Search for the one resource a question is about, the way the Omnibar
// searches: workloads before the objects they own.
import { useEffect, useId, useMemo, useState } from "react";
import { useDebouncedValue } from "@skyhook-io/k8s-ui";

import { type SearchResult, useSearch } from "../../api/client";
import {
  PENDING_RETRY_BUDGET_MS,
  PENDING_RETRY_MS,
  searchCoverageNote,
} from "../ui/searchCoverageNote";
import type { QuestionPin } from "./questionSuggestions";
import { formatInvestigationKind } from "./target";

function stillSyncing(data: SearchResult | undefined): boolean {
  return !!data?.coverage?.gaps.some((gap) => gap.code === "kind_sync_pending");
}

// Kinds that are usually children of what an engineer means: a question is
// about the Deployment or Service, not one of its ReplicaSets or Pods. They
// stay pickable, after everything else.
const OWNED_KINDS = new Set([
  "Pod",
  "ReplicaSet",
  "ControllerRevision",
  "EndpointSlice",
  "Event",
]);

function rankForPinning<T extends { kind: string }>(hits: T[]): T[] {
  return [...hits].sort(
    (a, b) => Number(OWNED_KINDS.has(a.kind)) - Number(OWNED_KINDS.has(b.kind)),
  );
}

export function ResourcePicker({
  inputRef,
  onPick,
  onCancel,
}: {
  inputRef: React.RefObject<HTMLInputElement | null>;
  onPick: (pin: QuestionPin) => void;
  onCancel: () => void;
}) {
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const listId = useId();
  // Search the way the Omnibar does: once typing pauses, not per keystroke.
  const searched = useDebouncedValue(query, 150, (value) => !value.trim());
  // Kinds still syncing answer "not yet", not "no": keep asking, as the
  // Omnibar does, for a bounded while.
  const [exhausted, setExhausted] = useState<string | null>(null);
  const { data, isFetching, isPlaceholderData, isError, refetch } = useSearch(
    searched,
    {
      context: "none",
      limit: 8,
      globalNs: true,
      refetchInterval: (latest) =>
        stillSyncing(latest) && exhausted !== searched
          ? PENDING_RETRY_MS
          : false,
    },
  );
  const syncing = !isPlaceholderData && stillSyncing(data);
  useEffect(() => {
    if (!syncing) return;
    const timer = setTimeout(
      () => setExhausted(searched),
      PENDING_RETRY_BUDGET_MS,
    );
    return () => clearTimeout(timer);
  }, [syncing, searched]);
  // Results belong to what is in the box only once it has been searched.
  const current =
    searched === query && query.trim().length >= 2 && !isPlaceholderData;
  // Results kept from the previous query while the next one loads must not
  // be pinnable: Enter would pin what the engineer just typed away from.
  const hits = useMemo(
    () => (current && !isError ? rankForPinning(data?.hits ?? []) : []),
    [current, isError, data],
  );
  const note = current
    ? searchCoverageNote(data?.coverage, {
        hits: data?.hits,
        query,
        pendingExhausted: exhausted === searched,
      })
    : undefined;
  const noteText = note?.parts
    ? note.parts.map((part) => part.text).join(" ")
    : note?.text;
  const activeIndex = Math.min(active, Math.max(hits.length - 1, 0));
  const pick = (hit: (typeof hits)[number]) =>
    onPick({
      kind: hit.kind,
      group: hit.group ?? "",
      namespace: hit.namespace ?? "",
      name: hit.name,
    });
  return (
    <div className="border-t border-theme-border-subtle px-3 py-2">
      <input
        ref={inputRef}
        value={query}
        onChange={(event) => {
          setQuery(event.target.value);
          setActive(0);
        }}
        onKeyDown={(event) => {
          if (event.key === "Escape") {
            event.preventDefault();
            event.stopPropagation();
            onCancel();
          } else if (event.key === "ArrowDown" || event.key === "ArrowUp") {
            event.preventDefault();
            if (hits.length === 0) return;
            const step = event.key === "ArrowDown" ? 1 : -1;
            setActive((activeIndex + step + hits.length) % hits.length);
          } else if (event.key === "Enter") {
            event.preventDefault();
            const hit = hits[activeIndex];
            if (hit) pick(hit);
          }
        }}
        role="combobox"
        aria-expanded={hits.length > 0}
        aria-label="Search for a resource to pin"
        aria-controls={listId}
        aria-activedescendant={
          hits.length > 0 ? `${listId}-${activeIndex}` : undefined
        }
        placeholder="Search resources by name, kind:Deployment, ns:shop…"
        className="w-full rounded-md border border-theme-border bg-theme-elevated px-2 py-1 text-sm text-theme-text-primary placeholder:text-theme-text-tertiary focus:border-accent focus:outline-none"
      />
      <ul id={listId} role="listbox" className="mt-1 max-h-48 overflow-y-auto">
        {hits.map((hit, index) => (
          <li
            key={`${hit.group ?? ""}/${hit.kind}/${hit.namespace ?? ""}/${hit.name}`}
            id={`${listId}-${index}`}
            role="option"
            aria-selected={index === activeIndex}
          >
            <button
              type="button"
              tabIndex={-1}
              onClick={() => pick(hit)}
              onMouseEnter={() => setActive(index)}
              className={`flex w-full items-baseline gap-2 rounded px-2 py-1 text-left text-sm ${
                index === activeIndex ? "bg-theme-hover" : ""
              }`}
            >
              <span className="truncate text-theme-text-primary">
                {hit.name}
              </span>
              <span className="shrink-0 text-xs text-theme-text-tertiary">
                {formatInvestigationKind(hit.kind, hit.group ?? "")}
                {hit.namespace ? ` · ${hit.namespace}` : ""}
              </span>
            </button>
          </li>
        ))}
        {current && isError && !isFetching && (
          <li className="flex items-center gap-2 px-2 py-1 text-xs text-theme-text-secondary">
            Couldn’t search resources.
            <button
              type="button"
              onClick={() => void refetch()}
              className="font-medium text-theme-text-primary hover:underline"
            >
              Try again
            </button>
          </li>
        )}
        {current && !isError && !isFetching && hits.length === 0 && (
          <li className="px-2 py-1 text-xs text-theme-text-tertiary">
            {noteText
              ? "No matches in what Radar searched."
              : "No matching resources."}
          </li>
        )}
        {current && !isError && noteText && (
          <li className="px-2 py-1 text-[11px] text-theme-text-tertiary">
            {noteText}
            {syncing && exhausted === searched && (
              <button
                type="button"
                onClick={() => {
                  setExhausted(null);
                  void refetch();
                }}
                className="ml-1 font-medium text-theme-text-primary hover:underline"
              >
                Try again
              </button>
            )}
          </li>
        )}
      </ul>
      <p className="mt-1 text-[11px] text-theme-text-tertiary">
        Pinning starts the investigation at that resource, and lets you apply a
        fix from it.
      </p>
    </div>
  );
}
