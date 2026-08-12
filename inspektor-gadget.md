# Inspektor Gadget × Radar — research notes (2026-07-19)

> Settled implementation plan (post cross-review): `inspektor-gadget-plan.md`
> Note: IG ships an official MCP server (`inspektor-gadget/ig-mcp-server`) and AKS's MCP
> server exposes IG — "MCP nobody has" is stale; differentiation is in-context curation +
> combined K8s-API+kernel tool surface. AKS Desktop (Headlamp-based) also ships IG UI.

## What IG is

CNCF sandbox project (Kinvolk 2019 → Microsoft since April 2021 acquisition). Packages eBPF
programs as OCI-distributed "gadgets" (~50: trace_dns, trace_tcp, trace_exec, trace_open,
tcpdump, top_tcp/top_file/top_process, profile_cpu, trace_ssl/trace_sni, audit_seccomp,
advise_networkpolicy, snapshot_*). Maps kernel events to K8s pods/containers automatically.
On K8s: privileged DaemonSet exposing gRPC; consumed via `kubectl gadget` CLI, embeddable Go
library, or gRPC directly. Ships as a supported AKS add-on (main distribution channel).
First independent security audit completed mid-2026 (OSTIF/CNCF).

## Strategic take

- **Not a competitor** — IG is a kernel-level sensor framework with no UI of its own; Radar is
  an API-server-level cockpit. Complementary by construction. Only touchpoints: traffic maps
  (IG would be a 4th TrafficSource next to Caretta/Hubble/Istio — fallback, not headline),
  advise_networkpolicy (a feature to consume, not compete with), audit (name collision only —
  config-scan vs runtime syscalls; could compose).
- **Headline value**: deep pod/node live debugging Radar can't do today (DNS/exec/file/TCP
  traces, tcpdump, CPU profiles on the pod detail page) + MCP tools ("trace this pod's DNS")
  for the AI-debugging story. Nobody in the kubectl-dashboard space has the MCP+eBPF combo.
- **Tension**: IG needs a privileged eBPF DaemonSet — against Radar's zero-footprint pitch.
  Shape must be: detect-if-present first, progressive disclosure, opt-in install
  recommendation (existing `Recommendation`/`HelmChartInfo` machinery), never a prerequisite.
- **Prior art / competitive pressure**: Headlamp (also Kinvolk-born, also Microsoft) ships an
  official IG plugin (Gadgets sidebar + pod/node embedded views). Validation + parity risk.
  Radar's angle: curate 4-5 gadgets into debugging journeys + MCP, not a generic gadget browser.
- **Radar has the exact slot**: `pkg/traffic/source.go` TrafficSource (Detect → recommend →
  Connect via port-forward gRPC — Hubble already does precisely this pattern).
- **Precedent for commercial vendors building on IG**: ARMO's Kubescape embeds IG as a
  library; their engineer holds an IG maintainer seat.

## Who to talk to

Maintainers (MAINTAINERS.md), commit counts = 2026 YTD:

| Person | GitHub | Role | Why |
|---|---|---|---|
| Maya Singh | mayasingh17 | Project manager (Microsoft PM) | Co-authored Headlamp-plugin announcement; cleanest door for co-announcement/ecosystem asks |
| Chris Kühl | blixtra | Project manager; leads Headlamp; ex-Kinvolk CEO | Champions UI integrations; also the channel-conflict person (Headlamp) |
| Mauricio Vásquez Bernal | mauriciovasquezbernal | Project lead, global CODEOWNER, docs owner (64 commits) | Technical authority: gRPC API, Go library, stability guarantees |
| Alban Crequy | alban | Creator (Kinvolk 2019), global CODEOWNER (48 commits) | Same as above |
| Qasim Sarfraz | mqasimsarfraz | Maintainer; owns Helm charts; commits in headlamp-plugin repo | **Bridge person** — has wired IG into a dashboard from the IG side; best first technical contact |
| Matthias Bertschy | matthyx | Maintainer, at ARMO (not Microsoft) | **Kindred spirit** — Kubescape embeds IG; candid view of the outside-vendor relationship; consult on library-vs-gRPC |
| Francis Laniel | eiffel-fl | Maintainer (95 commits, CI/infra) | Thread routing |
| Burak Ok | burak-ok | Maintainer (198 commits — top human committer) | Thread routing |
| Michael Friese | flyth | Maintainer (cmd/common) | CLI/common |
| Jose Blanquicet | blanquicet | Maintainer (cmd/ig) | ig CLI |

Channels:
- `#inspektor-gadget` on Kubernetes Slack (primary, responsive)
- Community meetings — open agenda, calendar: https://zoom-lfx.platform.linuxfoundation.org/meetings/inspektorgadget?view=month

## Approach sequence

Show up with something, not an ask:
1. Prototype first (pod-detail trace panel).
2. Demo GIF + design questions in `#inspektor-gadget` Slack.
3. Community-meeting demo slot.
4. Then Maya/Chris for co-announcement — arrive as an adoption win, not a distribution ask.
5. Early: talk to Matthias Bertschy about library-vs-gRPC consumption path.

## Sources

- https://github.com/inspektor-gadget/inspektor-gadget/blob/main/MAINTAINERS.md
- https://github.com/inspektor-gadget/inspektor-gadget/blob/main/CODEOWNERS
- https://inspektor-gadget.io/blog/2025/03/inspektor-gadget-plugin-for-headlamp/
- https://github.com/inspektor-gadget/headlamp-plugin
- https://inspektor-gadget.io/docs/latest/gadgets/
- https://www.cncf.io/blog/2026/06/03/inspektor-gadget-results-from-the-first-security-audit/
