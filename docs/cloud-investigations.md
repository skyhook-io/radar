# Cloud investigation integration handoff

This is an integration checklist for hosts consuming `@skyhook-io/radar-app`'s
investigation UI, not a claim that hosted Findings support is implemented.
The [TypeScript API contract](../web/src/api/diagnose.ts) is the wire source of
truth; the examples and tests linked below specify behavior without introducing
a second schema or runtime package.

## Scope and release boundary

- Updated Radar servers expose the public [MCP evidence limitations](mcp.md#diagnose-evidence-limits).
  Hosts must preserve collection failures, sampling, truncation and source-specific
  timestamps. Tool completion does not establish atomic observation or freshness.
- Findings additionally needs trusted tool results, durable investigation events
  and server-validated citations. A frontend dependency upgrade alone supplies
  none of those backend guarantees.
- Keep Cloud's read-only execution policy. Integrating Findings does not require
  enabling Apply, mutation outcomes or automatic post-mutation verification.
- Cloud backend/worker changes, consumer migration, package publication and hosted
  end-to-end validation are separate work. No release version is selected here.

## Required contract alignment

| Surface | Host responsibility |
|---|---|
| Target identity | Carry `group`, `kind`, `namespace`, `name` from launch through persistence and `RunSummary`. `group: ""` means core, not unknown. Preserve cluster/org isolation; resolve missing identity from authoritative resource information, not a blanket core-group default. |
| Run API | Match the client paths and response shapes for create/list/get-by-id, follow-up, stop and history. Preserve hosted visibility/continuation permissions where supported. Deep links must resolve a run directly rather than depend on a bounded history list. |
| Tool steps | Keep running/done `id` association, tool identity and input arguments. Retain bounded usable results, with truthful `isError` and `truncated` metadata. An arbitrary short string prefix usually cannot be parsed into evidence. |
| Provenance | Set `radarEvidence` only after server-side validation of the producing Radar result. Persist it and `evidenceRef` with the retained step. Neither a familiar tool name nor a model-emitted marker proves provenance. |
| Assessment citations | Validate requested refs server-side and emit `rootCauseEvidence` as `linked`, `missing` or `invalid`. Missing/invalid linkage must not turn into invented source attribution. |
| Replay | Preserve positive durable SSE `id` values. Send an unsequenced `replay_complete` after each connection's backlog and before its live tail. Do not count replayed evidence as newly arrived evidence. |
| History failure | Use `history_unavailable` with an explicit retryability decision. Preserve the reconnect cursor on retryable failure; terminal failure must not silently present an empty transcript. `closed` means the run is no longer usable, not merely that one turn completed. |
| Explanation | Accept `{ "explainAssessment": <done-event sequence> }` independently of a free-text question. Validate the originating assessment and permission to continue; persist that origin on the explanation turn. Return prose without promoting it to a replacement diagnosis. |

### Provenance and retention rules

The local implementation uses a private per-turn registry:
[issuance](../internal/investigationrefs/registry.go) and
[binding](../internal/ai/runs.go). These are reference implementations, not an
internal Go API that a separate host can import.

A hosted implementation must bind the authoritative producer payload to the
retained tool result within the correct authorized run/turn. Preserve redaction
and enforce retention limits before exposing or storing data. Do not merely
increase result retention without considering sensitive data and storage bounds.

The current UI accepts 1–3 unique citation refs, each matching
`ev_[a-z2-7]{26,128}_[a-z2-7]{26,128}`, all sharing one turn scope. Each must resolve
to exactly one complete, successful current-turn result. A malformed, foreign,
ambiguous, failed or truncated ref invalidates the requested set. These checks
also apply server-side; the frontend is not the trust boundary.

Do not manufacture provenance for pre-integration saved transcripts. Define their
honest read-only/Activity presentation during the Cloud migration rather than
stamping old model-visible output as verified evidence.

### Explain simply and capabilities

Reuse [assessment-bound explanation behavior](../internal/ai/explanation.go):
on-demand generation, origin validation, local loading/result/error presentation
and no assessment replacement. Prompting against new investigation work is not a
guarantee of tool-free execution. If a host wants that guarantee, enforce it at
the execution boundary; do not hide actual newly collected evidence from freshness
checks. Do not expose an unsupported explanation action during rollout; explicit
capability gating would be a separate integration change, not an existing API.

Keep hosted consent copy and `hosted` agent framing via the existing
[customization surface](../web/src/context/DiagnoseCustomization.tsx). Do not reuse
claims about local execution, local transcript storage or the user's CLI billing
for a hosted runner.

## Known consumer migration work

At handoff, the inspected Radar Hub implementation needs these changes:

- `radar-hub-web`: Fleet launch target construction must preserve API group;
  audit both Fleet and embedded cluster-view entry points.
- `radar-hub`: request/run identity, worker result retention, provenance/citation
  validation, replay boundaries and assessment-bound explanations need alignment.
  The inspected worker retains only a 600-character result prefix; its current
  turn handler requires a question and does not implement `explainAssessment`.
- Keep existing organization/cluster access, transcript visibility, managed billing,
  consent and read-only enforcement. Protocol alignment must not bypass them.

This is a point-in-time migration list, not a maintained description of Cloud's
implementation. Recheck the consumer before making changes.

## Package release gates

1. Treat required `DiagnoseTarget.group` as a breaking change for versioned
   consumers. Choose the appropriate breaking release and migrate target builders;
   do not restore ambiguity by making the field optional just to compile.
2. Publish the matching `@skyhook-io/k8s-ui` first. Findings imports the newly
   exported `defaultConditionTone`; set `radar-app`'s peer floor to a published
   version that includes it, following the
   [publication workflow](../.github/workflows/publish-radar-app.yml).
3. Build the consumer against the actual selected package versions, not just local
   workspace aliases. Verify both package stylesheets/Tailwind source scanning,
   hosted theme overrides and the existing provider mounting order.
4. Roll out the compatible backend before exposing the upgraded investigation UI.
   Publishing packages, deploying Cloud and merging the OSS PR are distinct gates.

## Existing executable examples

| Contract | Tests to reuse as examples |
|---|---|
| Exact issuance, scope isolation, forged/mixed refs | [Registry tests](../internal/investigationrefs/registry_test.go), [citation binding tests](../internal/ai/runs_evidence_test.go) |
| Persisted provenance and replay boundary/failure behavior | [Server stream tests](../internal/server/ai_diagnose_stream_test.go), [client SSE tests](../web/src/api/diagnose.test.ts) |
| Explanation origin, validation and non-promotion | [Explanation tests](../internal/ai/explanation_test.go), [UI explanation tests](../web/src/components/diagnose/investigationExplanation.test.ts) |
| Evidence relevance and incomplete collection | [Projection tests](../web/src/components/diagnose/investigationEvidence.test.ts), [pane tests](../web/src/components/diagnose/InvestigationEvidencePane.test.tsx) |
| Meaningful history, not timestamp-only repetition | [Presentation tests](../web/src/components/diagnose/investigationEvidencePresentation.test.ts) |

## Cloud acceptance checklist

- [ ] Launch the same target from Fleet and cluster views; distinguish core/CRD
  same-kind identities and never carry a run across unauthorized cluster/org scope.
- [ ] Open a saved run by deep link, refresh and reconnect during a live turn;
  replay does not duplicate turns, animate old arrivals or lose source targets.
- [ ] Relevant evidence appears; unrelated cluster-wide results stay in Activity.
  Source navigation opens the correct retained tool result and best-effort excerpt.
- [ ] Failed collection and truncated results remain qualifications, including for
  healthy assessments; forged/foreign/ambiguous refs never gain trusted attribution.
- [ ] Explain simply stays attached to its assessment across completion and replay;
  loading, failure/retry and permissions are honest, without nested assessments.
- [ ] Private/shared runs enforce access on reads, replay and continuation; consent
  copy and managed execution/billing claims remain accurate.
- [ ] Apply remains unavailable in read-only Cloud. Stop, worker failure and history
  failure produce accurate terminal/retry states rather than apparent success.
- [ ] Both host layouts render correctly at narrow/wide widths and in light/dark
  mode, using published packages. Pre-integration history is not mislabelled trusted.
