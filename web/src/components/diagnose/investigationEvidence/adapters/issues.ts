import { type Issue, type IssueRecentChange } from "@skyhook-io/k8s-ui";
import { type DiagnosisDNSContext } from "../../diagnoseEvidenceTypes";
import {
  ProjectionBuilder,
  addChanges,
  addIssueObservation,
  addNarrowHint,
  evidenceTierForRelevance,
  invalidPayload,
  scopeFromArgs,
  sourceArgsRelevance,
} from "../observations";
import {
  issue,
  nonEmptyString,
  parseJSON,
  recentChange,
  record,
  stringArray,
} from "../parse";
import { type InvestigationEvidenceSource } from "../types";

export function adaptIssues(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  payload: unknown,
): void {
  const value = record(payload);
  const raw = value?.issues;
  if (
    !value ||
    !Array.isArray(raw) ||
    typeof value.total !== "number" ||
    typeof value.total_matched !== "number"
  ) {
    invalidPayload(builder, source);
    return;
  }
  const issues = raw.map(issue).filter((item): item is Issue => Boolean(item));
  if (issues.length !== raw.length) {
    invalidPayload(builder, source);
    return;
  }
  addNarrowHint(builder, source, value);
  if (
    value.total_matched > issues.length &&
    !nonEmptyString(value.narrowHint)
  ) {
    builder.limit(
      source,
      "Issues",
      `The query returned ${issues.length} of ${value.total_matched} matching issues.`,
      "truncated",
    );
  }
  if (typeof value.filter_errors === "number" && value.filter_errors > 0) {
    builder.limit(
      source,
      "Issues filter",
      nonEmptyString(value.filter_error_sample)
        ? value.filter_error_sample
        : `${value.filter_errors} issue rows could not be evaluated by the filter.`,
      "error",
    );
  }
  if (value.recent_changes_truncated === true) {
    builder.limit(
      source,
      "Issue-related changes",
      "The issue response omitted some recent changes.",
      "truncated",
    );
  }
  if (value.correlation_truncated === true) {
    builder.limit(
      source,
      "Issue change correlation",
      "Change correlation was not evaluated for every returned issue.",
      "truncated",
    );
  }
  const visibility = record(value.visibility);
  if (
    visibility &&
    nonEmptyString(visibility.state) &&
    visibility.state !== "ok"
  ) {
    builder.limit(
      source,
      "Issue visibility",
      nonEmptyString(visibility.impact)
        ? visibility.impact
        : `Radar reported ${visibility.state} visibility for this issue query.`,
      "unknown",
    );
  }
  const issueChangesRaw = value.recent_changes;
  if (Array.isArray(issueChangesRaw)) {
    const changes = issueChangesRaw
      .map(recentChange)
      .filter((item): item is IssueRecentChange => Boolean(item));
    if (changes.length === issueChangesRaw.length) {
      addChanges(
        builder,
        source,
        changes,
        `changes:issues:${source.args ?? scopeFromArgs(source)}`,
        undefined,
        value.recent_changes_truncated !== true,
      );
    } else {
      invalidPayload(builder, source, "Issue-related changes");
    }
  } else if (issueChangesRaw !== undefined) {
    invalidPayload(builder, source, "Issue-related changes");
  }
  const clusterDNS = record(record(value.cluster_context)?.dns);
  if (clusterDNS) {
    const signals = stringArray(clusterDNS.signals) ?? [];
    const findings = Array.isArray(clusterDNS.findings)
      ? clusterDNS.findings
      : [];
    if (signals.length > 0 || findings.length > 0) {
      builder.observe(`dns:issues:${scopeFromArgs(source)}`, "dns", source, {
        tier: "context",
        relevance: "broader",
        tone: "warning",
        title: "Cluster DNS signals",
        summary:
          signals[0] ||
          `${findings.length} CoreDNS finding${findings.length === 1 ? "" : "s"}`,
        data: {
          type: "dns",
          dns: {
            signals,
            coreDNSFindings: findings as DiagnosisDNSContext["coreDNSFindings"],
          },
        },
      });
    }
  }
  if (issues.length === 0) {
    if (!source.confirmedSuccess || builder.limitedSources.has(source.id))
      return;
    const args = record(source.args ? parseJSON(source.args) : undefined);
    if (!nonEmptyString(args?.namespace)) {
      builder.limit(
        source,
        "Issues",
        "Radar found no matching issues, but the search covered only namespaces the current user can access.",
        "unknown",
      );
      return;
    }
    const scope = scopeFromArgs(source);
    const relevance = sourceArgsRelevance(builder, source);
    builder.observe(`issues:${source.args ?? scope}`, "receipt", source, {
      tier: evidenceTierForRelevance("checked", relevance),
      relevance,
      tone: "neutral",
      title: "No live issues matched this search",
      summary: scope,
      data: {
        type: "receipt",
        checked: "issues",
        scope,
      },
    });
  } else {
    for (const item of issues) addIssueObservation(builder, source, item);
  }
}
