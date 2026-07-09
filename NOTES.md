# Autodev Notes

## 2026-07-10 - batch workload rebase over #1106

- Assumption: PR #1106's redesigned application/workload scope model is the base design; batch support should extend the new Overview/Topology/History and workload-scope surfaces instead of restoring the older rail/top-band layout.
- Decision: Keep operational Issues in the app Overview by embedding the existing Issues row renderer scoped to app workloads, not by creating a separate incident-summary component.
- Decision: Preserve pod log health metadata from main and add Argo workflow step metadata as separate fields (`stepID`, `stepName`, `stepPhase`) so workflow context does not overwrite Kubernetes pod phase.
- Decision: Add only additive shared UI props for batch Overview overrides and app Overview issue slots; do not remove or rename existing `@skyhook-io/k8s-ui` exports.
