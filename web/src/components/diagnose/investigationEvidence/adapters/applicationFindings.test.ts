import { describe, expect, it } from "vitest";
import {
  investigationEvidenceSubjectRef,
  projectInvestigationEvidence,
} from "../index";
import {
  criticalIssue,
  deployment,
  evidenceRef,
  groupsOf,
  project,
  target,
  tool,
} from "../evidenceFixtures";

const connector = {
  ...criticalIssue,
  id: "kafka.strimzi.io:KafkaConnector:shop:sink:StrimziConnectorTasksFailed",
  severity: "warning",
  group: "kafka.strimzi.io",
  kind: "KafkaConnector",
  name: "sink",
  reason: "StrimziConnectorTasksFailed",
  message: "Strimzi reports one or more connector tasks as FAILED",
};
const source = {
  group: "kafka.strimzi.io",
  kind: "KafkaConnect",
  namespace: "shop",
  name: "connect",
};
const context = {
  tier: "basic",
  workloadSummary: { replicas: { desired: 1, ready: 1, available: 1 } },
  relatedApplicationFindings: {
    source,
    findings: [connector],
    coverage:
      "Checked 1 authorized cached connector snapshots; missing findings do not establish application health.",
  },
};

describe("cached application findings", () => {
  it.each(["diagnose", "get_resource"])(
    "keeps connector identity and citation separate from healthy parent in %s",
    (name) => {
      const ref = evidenceRef("a", "b");
      const result = project([
        tool(
          "read",
          name,
          { resource: deployment, resourceContext: context },
          { evidenceRef: ref },
        ),
      ]);
      const finding = groupsOf(result.groups, "issue")[0];
      expect(finding.latest).toMatchObject({
        tier: "supporting",
        relevance: "producer-related",
        source: { evidenceRef: ref },
        data: { issue: connector, applicationSource: source },
      });
      expect(investigationEvidenceSubjectRef(finding.latest.data)).toMatchObject({
        group: "kafka.strimzi.io",
        kind: "KafkaConnector",
        namespace: "shop",
        name: "sink",
      });
      expect(groupsOf(result.groups, "resource")[0].latest).toMatchObject({
        tier: "context",
        tone: "neutral",
        summary: "1/1 replicas ready",
      });
      expect(
        result.limitations.some((item) =>
          item.message.includes("do not establish application health"),
        ),
      ).toBe(true);
      expect(groupsOf(result.groups, "receipt")).toHaveLength(0);
    },
  );

  it("does not clear captured connector proof when a later parent diagnosis lacks it", () => {
    const result = projectInvestigationEvidence(
      [
        {
          timeline: [
            tool("read", "diagnose", {
              resource: deployment,
              resourceContext: context,
            }),
          ],
        },
        {
          verify: true,
          timeline: [
            tool("verify", "diagnose", {
              resource: deployment,
              relatedIssues: [],
            }),
          ],
        },
      ],
      target,
    );
    expect(groupsOf(result.groups, "issue")[0].historical).toBe(false);
  });

  it("shows incomplete inspection without emitting a healthy receipt", () => {
    const result = project([
      tool("read", "diagnose", {
        resource: deployment,
        resourceContext: {
          tier: "basic",
          relatedApplicationFindings: {
            source,
            truncated: true,
            coverage:
              "Connector findings were not checked because this namespace exceeds the inspection budget.",
          },
        },
      }),
    ]);
    expect(groupsOf(result.groups, "issue")).toHaveLength(0);
    expect(groupsOf(result.groups, "receipt")).toHaveLength(0);
    expect(result.limitations).toEqual(
      expect.arrayContaining([expect.objectContaining({ kind: "truncated" })]),
    );
  });

  it("rejects malformed findings without projecting partial proof", () => {
    const result = project([
      tool("read", "get_resource", {
        resource: deployment,
        resourceContext: {
          ...context,
          relatedApplicationFindings: {
            ...context.relatedApplicationFindings,
            findings: [connector, { id: "bad" }],
          },
        },
      }),
    ]);
    expect(groupsOf(result.groups, "issue")).toHaveLength(0);
    expect(
      result.limitations.some(
        (item) => item.source === "Related application findings",
      ),
    ).toBe(true);
  });
});
