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
    coverageState: "cached",
    checked: 1,
    unknown: 0,
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
      expect(
        investigationEvidenceSubjectRef(finding.latest.data),
      ).toMatchObject({
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
      expect(
        groupsOf(result.groups, "receipt").filter(
          (group) =>
            group.latest.data.type === "receipt" &&
            group.latest.data.checked === "issues",
        ),
      ).toHaveLength(0);
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
          status: "done",
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
            coverageState: "limited",
            checked: 0,
            unknown: 0,
            truncated: true,
            coverage:
              "Connector findings were not checked because this namespace exceeds the inspection budget.",
          },
        },
      }),
    ]);
    expect(groupsOf(result.groups, "issue")).toHaveLength(0);
    expect(
      groupsOf(result.groups, "receipt").filter(
        (group) =>
          group.latest.data.type === "receipt" &&
          group.latest.data.checked === "issues",
      ),
    ).toHaveLength(0);
    expect(result.limitations).toEqual(
      expect.arrayContaining([expect.objectContaining({ kind: "truncated" })]),
    );
  });

  it.each(["KafkaConnector", "KafkaConnect"])(
    "does not invent event or change reads for cached %s diagnosis",
    (kind) => {
      const result = project([
        tool("read", "diagnose", {
          resource: {
            apiVersion: "kafka.strimzi.io/v1",
            kind,
            metadata: { namespace: "shop", name: "connect" },
          },
          resourceContext: { tier: "basic" },
          coverage:
            "Cached operator evidence only; Pods, logs and live application endpoints were not collected.",
        }),
      ]);
      expect(groupsOf(result.groups, "receipt")).toHaveLength(0);
    },
  );

  it("still retires the parent's own repaired issue while retaining connector proof", () => {
    const result = projectInvestigationEvidence(
      [
        {
          timeline: [
            tool("read", "diagnose", {
              resource: deployment,
              relatedIssues: [criticalIssue],
              resourceContext: context,
            }),
          ],
        },
        {
          verify: true,
          status: "done",
          timeline: [
            tool("verify", "diagnose", {
              resource: deployment,
              relatedIssues: [],
              resourceContext: {
                ...context,
                relatedApplicationFindings: {
                  ...context.relatedApplicationFindings,
                  findings: [],
                },
              },
            }),
          ],
        },
      ],
      target,
    );
    expect(
      groupsOf(result.groups, "issue").find(
        (group) => group.identity === `issue:${criticalIssue.id}`,
      )?.historical,
    ).toBe(true);
    expect(
      groupsOf(result.groups, "issue").find(
        (group) => group.identity === `issue:${connector.id}`,
      )?.historical,
    ).toBe(false);
  });

  it.each([true, false])(
    "retires a connector target only with explicit observed recovery (%s)",
    (recovered) => {
      const resource = {
        apiVersion: "kafka.strimzi.io/v1",
        kind: "KafkaConnector",
        metadata: { namespace: "shop", name: "sink" },
      };
      const result = projectInvestigationEvidence(
        [
          {
            timeline: [
              tool("read", "get_resource", {
                resource: deployment,
                resourceContext: context,
              }),
            ],
          },
          {
            verify: true,
            status: "done",
            timeline: [
              tool("verify", "diagnose", {
                resource,
                resourceContext: { tier: "basic" },
                operatorSnapshot: {
                  state: recovered ? "recovered" : "unknown",
                  generationCurrent: recovered,
                  readiness: recovered ? "ready" : "unknown",
                  reason: recovered ? undefined : "stale",
                },
              }),
            ],
          },
        ],
        {
          group: "kafka.strimzi.io",
          kind: "KafkaConnector",
          namespace: "shop",
          name: "sink",
        },
      );
      expect(groupsOf(result.groups, "issue")[0].historical).toBe(recovered);
    },
  );

  it.each(["shop", "other"])(
    "uses connector-scoped recovery in a parent investigation (%s)",
    (namespace) => {
      const resource = {
        apiVersion: "kafka.strimzi.io/v1",
        kind: "KafkaConnector",
        metadata: { namespace, name: "sink" },
      };
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
            status: "done",
            timeline: [
              tool("verify", "diagnose", {
                resource,
                operatorSnapshot: {
                  state: "recovered",
                  generationCurrent: true,
                  readiness: "ready",
                },
                resourceContext: { tier: "basic" },
              }),
            ],
          },
        ],
        target,
      );
      expect(groupsOf(result.groups, "issue")[0].historical).toBe(
        namespace === "shop",
      );
    },
  );

  it("shows unverifiable operator status as a limitation", () => {
    const result = project([
      tool("read", "diagnose", {
        resource: {
          apiVersion: "kafka.strimzi.io/v1",
          kind: "KafkaConnector",
          metadata: { namespace: "shop", name: "sink" },
        },
        resourceContext: { tier: "basic" },
        operatorSnapshot: {
          state: "unknown",
          generationCurrent: false,
          readiness: "unknown",
          reason: "stale",
        },
      }),
    ]);
    expect(result.limitations).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          source: "Operator snapshot",
          message: expect.stringContaining("stale"),
        }),
      ]),
    );
  });

  it("shows KafkaConnect's own NotReady condition separately from connector findings", () => {
    const result = project([
      tool("read", "diagnose", {
        resource: {
          apiVersion: "kafka.strimzi.io/v1",
          kind: "KafkaConnect",
          metadata: { namespace: "shop", name: "connect" },
        },
        resourceContext: {
          tier: "basic",
          statusSummary: {
            phase: "NotReady",
            conditions: [{ type: "Ready", status: "False" }],
          },
        },
        operatorSnapshot: {
          state: "observed",
          generationCurrent: true,
          readiness: "not_ready",
        },
      }),
    ]);
    expect(groupsOf(result.groups, "resource")[0].latest).toMatchObject({
      tone: "warning",
      summary: "NotReady",
    });
    expect(groupsOf(result.groups, "issue")).toHaveLength(0);
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
