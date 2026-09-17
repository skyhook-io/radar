import { adaptListResources } from "./resource";
import { nonEmptyString, parseJSON, record, stringArray } from "../parse";
import { invalidPayload, type ProjectionBuilder } from "../observations";
import type { InvestigationEvidenceSource } from "../types";

// Helm releases, installed packages and search hits are inventories: rows
// with a kind, a name and a namespace, one line of status, and for a hit
// the text that matched. They render on the inventory card so a citation
// can name a row the way it names a resource in a listing.

export function adaptListHelmReleases(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  payload: unknown,
): void {
  if (!Array.isArray(payload)) {
    invalidPayload(builder, source);
    return;
  }
  const args = record(parseJSON(source.args ?? ""));
  const namespace = nonEmptyString(args?.namespace)
    ? args.namespace
    : undefined;
  adaptListResources(
    builder,
    source,
    payload.map((entry) => {
      const item = record(entry);
      if (!item) return entry;
      const chart = [item.chart, item.chartVersion]
        .filter((part) => nonEmptyString(part))
        .join(" ");
      return {
        kind: "HelmRelease",
        name: item.name,
        namespace: item.namespace,
        status: [item.status, chart ? chart : undefined]
          .filter(Boolean)
          .join(" · "),
        ...(nonEmptyString(item.healthIssue)
          ? { issue: item.healthIssue }
          : {}),
      };
    }),
    { title: namespace ? `Helm releases in ${namespace}` : "Helm releases" },
  );
}

export function adaptListPackages(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  payload: unknown,
): void {
  const value = record(payload);
  const rows = value?.packages;
  if (!value || !Array.isArray(rows)) {
    invalidPayload(builder, source);
    return;
  }
  const legend = record(value.sourceLegend) ?? {};
  const args = record(parseJSON(source.args ?? ""));
  const namespace = nonEmptyString(args?.namespace)
    ? args.namespace
    : undefined;
  adaptListResources(
    builder,
    source,
    rows.map((entry) => {
      const item = record(entry);
      if (!item) return entry;
      const sources = (stringArray(item.sources) ?? []).map((code) =>
        nonEmptyString(legend[code]) ? legend[code] : code,
      );
      const health = nonEmptyString(item.health)
        ? item.health.toLowerCase()
        : undefined;
      const status = [
        nonEmptyString(item.version) ? item.version : undefined,
        sources.length > 0 ? sources.join(", ") : undefined,
      ]
        .filter(Boolean)
        .join(" · ");
      return {
        kind: "Package",
        name: nonEmptyString(item.releaseName) ? item.releaseName : item.chart,
        namespace: item.namespace,
        status,
        ...(health === "degraded" || health === "unhealthy"
          ? { issue: health }
          : {}),
      };
    }),
    { title: namespace ? `Packages in ${namespace}` : "Installed packages" },
  );
}

export function adaptSearch(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  payload: unknown,
): void {
  const value = record(payload);
  const hits = value?.hits;
  if (!value || !Array.isArray(hits)) {
    invalidPayload(builder, source);
    return;
  }
  const args = record(parseJSON(source.args ?? ""));
  const query = nonEmptyString(args?.query) ? args.query : undefined;
  adaptListResources(
    builder,
    source,
    hits.map((entry) => {
      const item = record(entry);
      if (!item) return entry;
      const snippets = Array.isArray(item.snippets) ? item.snippets : [];
      const matched = Array.isArray(item.matched) ? item.matched : [];
      const first = record(snippets[0]);
      const site = record(matched[0]);
      const match = first
        ? `${nonEmptyString(first.path) ? `${first.path}: ` : ""}${nonEmptyString(first.snippet) ? first.snippet : ""}`
        : site && nonEmptyString(site.site)
          ? `matched ${site.site}`
          : undefined;
      return {
        kind: item.kind,
        name: item.name,
        namespace: item.namespace,
        ...(match ? { match } : {}),
      };
    }),
    { title: query ? `Search results for “${query}”` : "Search results" },
  );
}
