# Radar UX/diagnosis audit — implementation handoff

Self-contained work order for an implementing agent. Derived from a 37-finding visual-test
audit across 3 clusters (KIND gitops-demo, GCP nonprod, AWS nonprod). All decisions are
resolved below — do NOT re-litigate scope; implement as written. Cross-reviewed with Codex
over 2 rounds (converged).

## Ground rules (read first)
- Repo: `radar` (KoalaOps). Build: `make build` (frontend+embed+binary), `make tsc`, `make test`. Frontend
  package: `packages/k8s-ui` (source-only `@skyhook-io/k8s-ui`) + `web/`. **Do NOT `npm run lint` (broken).**
- Each PR below: own branch off `origin/main`, own tests, `/review-loop` (self + codex) → `/qa` → PR → `/fix-pr-loop`.
  Never merge (the human merges). No `Co-Authored-By` / Claude attribution in commits or PR bodies. No ticket/PR refs in code comments.
- Issue model recap (so the fixes make sense): detectors in `internal/k8s/detect*.go` emit `Detection{Reason, Message, Severity, Cause, Action, ...}`;
  `internal/issues/` composes/dedupes/groups; `pkg/issuesapi/` is the wire contract. Plain-language **Cause/Action** is what makes a
  good issue (PVC/Ingress have it; HPA/Argo/Helm don't — that's the diagnosis gap).
- **PR #1057 is already open + converged (incident_parent per-resource pointer). Do NOT touch it.**

---

# PR-1 · Frontend polish + cause-label (k8s-ui + web)
Branch e.g. `fix/issues-ui-polish`. Low-risk, frontend-only.

### B — drop the drawer role badge (finding B)
- **File:** `packages/k8s-ui/src/components/issues/ResourceIssuesSection.tsx` — the `CausalContext` block (~line 110) renders
  `diagnosticRoleLabel(ctx.role)` as a badge ("Possible cause" / "Affected" / "Grouped").
- **Do:** remove that role badge **in this drawer component only**. Keep the fact labels ("Blocked pods", "Backend pods",
  "Stalled autoscalers", etc. — they already convey direction). Leave `IssuesView`'s role badge untouched (it's a triage queue, role context is useful there).
- **Why:** on a *root* resource (e.g. a PVC) the candidate-role blast-radius fact reads "Possible cause" which is backwards (the PVC isn't
  its own cause). Codex: drop, don't relabel (any honest relabel — "this resource may be causing related issues" — is too long for the surface).

### A — reword the PVC "Pending" note (finding A, the blocker)
- **File:** `packages/k8s-ui/src/components/resources/renderers/PVCRenderer.tsx:53-59` — the blue `<AlertBanner variant="info" title="Pending — awaiting binding">`.
- **Current message** (wrong): "Not yet bound to a volume. This is normal while provisioning, and expected indefinitely for a
  WaitForFirstConsumer StorageClass until a Pod that mounts this claim is scheduled."
- **Problem:** when the PVC is Pending for a *real* reason (e.g. missing StorageClass — shown as a CRITICAL Operational Issue right above
  it), this note says "this is normal / expected indefinitely" — contradicting the critical issue, and it's the *louder* (full-blue) element.
- **Do:** reword to drop the false reassurance. Something like: "Pending — not yet bound to a volume. A PVC stays Pending while its volume
  is provisioned, or (for a WaitForFirstConsumer StorageClass) until a Pod that mounts it is scheduled. **If it stays Pending, check the
  StorageClass, its provisioner, and storage quota.**" The renderer is pure (no issue data) so this is copy-only.
- **Out of scope (→ ticket T-RENDERER-ALERTS):** the broader pattern of renderer AlertBanners duplicating Operational Issues, and
  host-signal suppression (passing an "operationalIssuePresent" flag through `ResourceRendererDispatch`). Note it; don't build it here.

### #7/#19/#32 — CRD group friendly-names + fallback
- **File:** `packages/k8s-ui/src/utils/api-resources.ts` (group→label map; also referenced in `resource-utils.ts`, `ResourcesView.tsx`).
- **Problem:** the Resources sidebar mixes friendly names (KARPENTER, GATEWAY API, CERT MANAGER, EXTERNAL SECRETS, DYNAMIC RESOURCE
  ALLOCATION) with **raw uppercased API groups** (POLICIES.KYVERNO.IO, NETWORKING.GKE.IO, APIREGISTRATION.K8S.IO, WARDEN.GKE.IO, CLOUD.GOOGLE.COM).
- **Do:** (1) extend the map for the common groups above; (2) add a **fallback formatter** so any unmapped group never renders a raw
  uppercased domain — e.g. strip the TLD (`policies.kyverno.io` → "Kyverno"; `networking.gke.io` → "GKE Networking"-ish or Title-Case the
  first label). Pick a sensible deterministic rule; the point is no raw `FOO.BAR.IO` in the sidebar.

### #27 — header icon-button a11y (nit)
- The top-right header icon buttons (theme toggle, terminal, help, debug) have **no accessible names** (a11y tree shows empty `button`).
  Add `aria-label`s. (Find them in `web/src` header / the component rendering the top-right cluster `2,496` GitHub-stars row + icon buttons.)

### #1b/#1c — issues list polish (nit)
- `IssuesView` list: (#1b) when `issue_timing === started_at_resource_creation` the age shows "since deploy" and *replaces* the relative
  age — keep the relative age and add "since deploy" as a tag/tooltip instead (8+ rows all reading "since deploy" loses recency signal).
  (#1c) category chips (Control plane / Scaling / …) are very low-contrast grey — bump contrast so they're usable as a scan axis.

**Tests:** `make tsc`; render tests for the drawer (no role badge, PVC note reworded). Visual-test the PVC drawer (no contradiction) +
the CRD sidebar labels (no raw groups).

---

# PR-2 · Detector precision (backend, internal/k8s) — HIGHEST real-cluster value
Branch e.g. `fix/detector-precision`. 4 bounded fixes. This is the batch that de-noises real cluster queues.

### #15 — webhook-backend-down: respect failurePolicy  (the big one)
- **File:** `internal/k8s/detect_missing_refs.go:587 DetectMissingWebhookRefs` (and `checkWebhookList` ~:640). Emits Critical for every
  Validating/Mutating webhook whose `clientConfig.service` is missing. On real EKS this **floods the queue** (8+ criticals, mostly benign).
- **Decision (RESOLVED):** read each webhook's `failurePolicy` from the unstructured config. `Fail` ⇒ **critical** (admission blocked);
  `Ignore` (or unset where the cluster default makes it benign) ⇒ **warning** (admission proceeds; only enforcement/mutation is disabled).
- **Impl gotcha (Codex):** one missing Service can back **multiple webhooks** within/across configs with **different** failurePolicies. The
  current dedupe keys on `svcNS/svcName`. Either include failurePolicy in the dedupe key, or **aggregate to the worst severity** across all
  webhooks referencing that Service (so result isn't iteration-order-dependent). Pick worst-severity aggregation.
- **Do NOT** special-case Kyverno/OPA/"guardrail" webhooks (keeping Ignore-but-security-critical) — that's a product decision → ticket
  T-WEBHOOK-GUARDRAIL. Ship the generic rule only.
- **Tests:** Fail→critical; Ignore→warning; unset→(cluster default, document choice); multi-webhook same-Service mixed policy → worst severity.

### #6 — kindnet / env-Service false positive
- **File:** `internal/k8s/detect_config.go` — the env-var→Service ref check (`envServiceRef` ~:98; message "Missing referenced Service" :270;
  already has an `envServiceRefHasCausalEvidence` gate that this case slipped through).
- **Problem:** flagged `CONTROL_PLANE_ENDPOINT=radar-gitops-demo-control-plane:6443` (a **node hostname**) as a missing Service. Note
  `kind-control-plane:6443` **is** Service-name-shaped, so "exclude non-Service-shaped" will NOT work.
- **Do:** suppress the finding when the referenced host matches a **Node name / hostname / internal address** (i.e. it's a node endpoint,
  not a Service). **Test:** a normal single-label Service ref (e.g. `my-svc:8080` → Service `my-svc`) still fires.

### #13 — scheduler message double-comma + jargon
- **File:** `internal/k8s/detect_scheduling.go` (`parseSchedulerMessage` ~:94, `classifyClause` ~:140, reconstruction `strings.Join` ~:305).
- **Problem:** message renders `"…no new claims to deallocate,, 1 node(s) no free host ports"` — a `,,` artifact from joining clauses with an
  empty element, plus raw scheduler jargon.
- **Do:** drop empty clauses before the join; prefer the plain-language clause text where `classifyClause` produces it.

### #5 — suppress moot volume-conflict while PVC unbound
- **Where:** `internal/issues/` dedupe (same family as the other `dedupe*` passes in `compose.go` step 2) — OR the volume-access-conflict
  detector. A `volume_access_conflict` ("RWO shared across N replicas") is **moot** while the PVC is `Pending`/unbound (it can't bind
  regardless of access mode). On `needs-storage` it stacked a 3rd issue under an already-unschedulable→PVC Deployment.
- **Do:** suppress `volume_access_conflict` for a workload whose mounted PVC is currently Pending/unbound. **Test:** conflict still fires when
  the PVC is Bound.

**Tests:** Go unit per item (`internal/k8s` + `internal/issues`). **Live-verify:** on AWS us-east-1-nonprod the webhook flood should drop to
warnings; on KIND the kindnet false-positive should disappear.

---

# PR-3 · Remediation copy order (backend) — finding C
Branch e.g. `fix/remediation-copy`.
- **Files:** `internal/k8s/detect_missing_refs.go` (`cmRefDiag`, `secretRefDiag`, the imagePullSecret + StorageClass actions),
  `internal/k8s/detect_config.go`, and the PVC pending action.
- **Problem:** remediations **lead with "Create <missing thing>"** — e.g. `Create StorageClass "does-not-exist"`, `Create Service "ai-temp"`.
  When the ref is a typo or an obsolete/abandoned route (537-day-old dangling Ingress), "create it" is the *least* likely correct action.
- **Do:** reorder action copy to lead with **"point `<field>` at an existing one"** and **"or remove the reference"**, with "create it" last.
  Pure copy; keep the parsed Cause unchanged. **Tests:** assert the action string order for each diag helper.

---

# PR-4 · Diagnosis copy: Argo + Helm (backend)
Branch e.g. `fix/diagnosis-copy`. (HPA is NOT here — see ticket T-HPA-DIAGNOSIS.)
- **#3 Argo ComparisonError:** strip the `rpc error: code = Unknown desc = …` gRPC envelope and surface the meaningful tail
  (e.g. "app path does not exist"). Locate in the GitOps error parsing (`internal/k8s/detect_gitops.go` / `pkg/gitops/insights`). The
  `guestbook-drift` "StuckDriftLoop" copy is the quality bar — match it.
- **#4 Helm condition-speak:** `"…not ready. status: InProgress, message: Available: 0/1 context deadline exceeded"` → plain language
  ("the release's workload didn't become ready in time"). Locate in the helm diagnosis path.
- **Tests:** before/after copy assertions on representative raw errors.

---

# PR-5 · Connection-error: auth-masked-as-timeout + auto-recover (backend + web)
Branch e.g. `fix/connection-error-auth`.
- **Files:** `internal/k8s/connection_state.go:87 ClassifyError` (already classifies auth/rbac/network/timeout/unknown; auth case ~:104-111
  matches "unauthorized", "token has expired", "exec plugin", "gke-gcloud-auth-plugin", etc.); `web/src/components/ConnectionErrorView.tsx:166`.
- **Problem (#21):** AWS SSO/exec-credential expiry surfaces as `cluster unreachable: context deadline exceeded` (a **timeout**) and is
  classified as timeout → the UI tells the user "cluster under heavy load / network latency / check cluster health" instead of "re-authenticate".
  (Contrast: a stopped kind cluster's `connection refused` classifies correctly as Network Unreachable — so the classifier works; the gap is
  auth-masked-as-timeout, esp. AWS where the `aws` exec plugin failure can manifest as a hang/timeout rather than an auth keyword.)
- **Do:** in `ClassifyError`, detect exec-credential/SSO failures that present as timeouts (inspect the exec-plugin error/stderr if available;
  match AWS SSO/`aws sso`/expired-token shapes) → classify **auth**, with copy pointing to re-login (and the right command).
- **#26b auto-recover:** the error view is **sticky** — after creds are restored (API returns 200) it doesn't clear until manual reload.
  Add retry/poll so `ConnectionErrorView` auto-recovers when connectivity returns. (Behavior change — test it.)
- **Tests/verify:** induce SSO-expiry (auth-as-timeout → "Authentication failed — re-login"); restore creds → view auto-recovers.

---

# PR-6 · Metrics absent-handling (frontend hooks/API)
Branch e.g. `fix/metrics-absent`.
- **Problem (#8):** opening the Node drawer throws ~3 JS console errors when metrics-server is absent. NOT NodeRenderer-specific — the
  node-metrics + node-history (and likely pod-metrics) hooks fetch unconditionally and error repeatedly.
- **Do:** at the **metrics hook/API layer** (find the `useNodeMetrics`/`useResourceMetrics`/history hooks in `web/src/api`), treat an absent
  metrics API (404 / "metrics not available") as a clean "no metrics available" empty-state — fetch once, no repeated console errors.
  Covers node AND pod metrics. **Verify:** Node + Pod drawers on a no-metrics-server cluster → 0 console errors, graceful empty-state.

---

# Tickets (file as Linear under "Radar Cloud Revamp"; design separately — NOT in the PRs above)

- **T-CHRONIC-ACUTE (HIGH) — #12.** The biggest real-cluster trust gap: dangling-ref/webhook criticals aged 84–881 days, and "failed"
  Helm/GitOps states months old, all sit flat-"Critical" with no aging/ack. Needs its **own /plan-loop**. Mechanism options to weigh:
  age-decay severity tint · a "stale" section/filter · manual acknowledge · auto-resolve when older-than-N + unchanged. Evidence:
  GCP/AWS queues dominated by ancient criticals.
- **T-HPA-DIAGNOSIS — #2.** Give HPA `cannot-scale` a plain-language Cause/Action **per condition shape** (NOT a blanket "metrics-server
  down" — `FailedGetResourceMetric` also means *missing pod resource requests*; see the #1047 missing-request exclusion in
  `diagnostic_context.go` for the shape distinction): metrics-API-unavailable ⇒ "metrics API not responding"; missing-request ⇒ "pods lack
  resource requests"; adapter ⇒ its own. `internal/k8s/detect_workload.go` emits only the raw Reason today.
- **T-DAEMONSET-UNSCHED — #14.** Downgrade DaemonSet partial-unschedulable from Critical ONLY when it's a duplicate pod-level symptom AND
  the DaemonSet's desired-vs-scheduled is otherwise satisfied (placement-by-design on tainted/affinity'd nodes). Do NOT blanket-downgrade
  (would hide real node-coverage loss). Test-heavy; needs the duplicate-symptom condition designed carefully.
- **T-AUDIT-UNUSED — #22a.** "Unused ConfigMap or Secret — 137 resources" dominates the Checks page and is a classic false-positive-prone
  check. Tighten detection (consumed-via-controller/projected/envFrom edges) and/or collapse-by-default / make opt-in. ("Potential secret in
  ConfigMap — 25" precision belongs here too.)
- **T-CHECKS-ISSUES-DEDUPE — #22b.** "Service has no matching pods" (a Check) overlaps "No endpoints" (an Issue) — decide ownership/dedupe
  across the two axes (CLAUDE.md says they must stay separate; this one is operational, not best-practice).
- **T-WEBHOOK-GUARDRAIL — #15 follow-up.** Decide whether security/policy webhooks (Kyverno/OPA/admission guardrails) with
  `failurePolicy: Ignore` should stay **critical** ("enforcement silently disabled") rather than the generic warning. Needs the product call
  on which webhook classes count.
- **T-RENDERER-ALERTS — A follow-up.** General pattern: resource renderers show their own AlertBanners that can duplicate/contradict the
  Operational Issues section. Design a host-signal so renderers suppress/soften status banners when a diagnosed issue already covers it.
- **#1 incident grouping / demotion — already deferred** (separate prior plan-loop). Unchanged.

---

# Verify-only (quick checks, not code — confirm whether real bugs)
- **#17** Many TLS secrets show "Expired" in the Secrets list (GCP dev) — confirm real cluster hygiene vs radar mis-reading `notAfter` on rotated certs.
- **#18** Secret drawer cert dates looked **inverted** (Not Before May 3 / Expires May 2) — verify the cert date display/parse.
- **#25** Namespace list shows red "Terminating" badges on several namespaces (argocd/automation) — verify real stuck-terminating vs noise.

# Highlights — keep as-is (quality bar; don't regress)
Secret redaction + inline cert-expiry (#17/#18 data), Pod/Deployment/HTTPRoute drawers, Checks remediation-queue model, RBAC sections (#25),
GitOps/Helm views, cluster picker, dark-mode parity, the connection-error *layout*, Service no-endpoints → "Affected · Backend pods" (#9b).

---

## Suggested order (sequencing is delegated; this is the default)
PR-2 (highest value) → PR-3 → PR-1 ∥ PR-4 → PR-5 → PR-6 → file tickets. Each PR independent; none blocks another except they all branch off
current `origin/main` (which already has #1056; #1057 pending). Full finding-by-finding locations: see **`AUDIT-FINDINGS.md`** (37 rows,
sibling of this file) + screenshots in `.playwright-mcp/vt-audit/` (23 shots, gitignored — present only in the audit checkout).
