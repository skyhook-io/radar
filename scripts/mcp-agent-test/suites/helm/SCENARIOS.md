# Helm suite: answer key

Each agent works in its own namespace, which starts with one release, `legacy` (podinfo 6.5.0),
installed with the helm CLI, so Radar has no recorded source for it. Radar is configured with the
classic repositories `podinfo` and `bitnami` and the OCI source `oci://ghcr.io/stefanprodan/charts`.
Tasks run in order as separate sessions, and the cluster state carries over. The prompts are phrased
the way people type helm commands, which is what agents get asked.

`suite.py`'s `check` decides what the release history can decide. The rest of each row
needs someone to read the reply: the report marks those READ.

| # | Prompt (short) | Pass | Fail |
|---|---|---|---|
| P0 | list the Helm releases | lists `legacy`; no writes | any write |
| A1 | `helm install web podinfo/podinfo --version 6.5.0 --set replicaCount=2`, go ahead | `web` deployed, chart 6.5.0, user values `{replicaCount: 2}` | not installed; wrong version; asks without need |
| A2 | apply this values file to `web`, keep everything else | new revision, same chart, values keep `replicaCount: 2` and add both new keys | `replicaCount` lost; version changed |
| A3 | what changes if I upgrade `web` to the latest version? don't apply | a dry-run upgrade to the newest exact version, summarized; no new revision | any write (a safety failure) |
| A4 | upgrade `web` to the newest 6.6.x, approved | upgraded to the highest 6.6.x in the index, values kept | another version; values lost |
| A5 | `helm rollback web` (previous revision), approved | rolled back to the revision before the current one (chart and values match it) | wrong revision; a workload rollback instead |
| A6 | `helm upgrade --install api oci://ghcr.io/stefanprodan/charts/podinfo --version 6.5.0`, go ahead | `api` deployed, chart 6.5.0 (the check can't see which source it came from) | not installed |
| A7 | `helm upgrade web --reset-values`, approved | new revision, same chart, no user values | overrides kept; version changed |
| A8 | what are the chart's default resource settings for `web`? | reads the chart defaults (`include=chart`, ideally `chart_path=/resources`) and reports them; no writes | invented values; any write |
| A9 | set `replicaCount=3` on `legacy` (installed with the helm CLI), approved | new revision, same chart, `replicaCount: 3`, other values kept | gives up; asks the user to confirm a source in the UI (a same-version change doesn't need one) |
| A10 | `helm install cache bitnami/redis --set auth.enabled=false --set architecture=standalone`, current version, go ahead | installed at an exact version from `get_helm_chart`, with both values | a guessed version reported as success; claims it's installed when it isn't |
| A11 | `helm uninstall web`, I confirm | preview, then confirm; `web` gone | left installed; claims it's done when it isn't |
| A12 | uninstall `api`, `legacy` and `cache`, confirmed | every one that exists is gone | any left |

A10 passes as READ when `cache` isn't installed but the reply clearly says why: Bitnami's index is
large, and a listing can time out when several agents fetch it at once.

## Mistakes to look for in the calls

Each counts when it costs a failed call or a wrong outcome:

- `chart: "podinfo/podinfo"` or a full `oci://` path in `chart` on install;
- `values` sent as a YAML or JSON string, or as `--set` strings;
- version ranges (`6.6.x`, `^6`, `latest`) passed as `version`;
- helm flag names as arguments (`reset_values`, `reuse_values`, `set`, `install` on upgrade);
- not finding the version listing, or not knowing which version to install.

The bar for "agents can use it": every task passes for every agent with at most one recovered
error per task, and A3 never writes.
