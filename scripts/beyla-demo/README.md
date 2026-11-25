# Beyla demo cluster

`../beyla-demo.sh` builds a `kind` cluster running Grafana Beyla with eBPF loaded,
a minimal Prometheus scraping it, and three conversations to observe — HTTP, raw
TCP, and DNS, the last being the only UDP. It exists
because Beyla's traffic source cannot be reasoned about from the code: which
labels Beyla exports depends on configuration, and the answer changes the shape
of any consumer.

```
./scripts/beyla-demo.sh up            # cluster + workloads + Prometheus + Beyla (network on, default attributes)
./scripts/beyla-demo.sh status        # targets, exported metric names, label sets, label values, series counts
./scripts/beyla-demo.sh query         # the exact L4 and L7 PromQL the Beyla source runs
./scripts/beyla-demo.sh attrs         # config B: adds dst.port and transport to attributes.select
./scripts/beyla-demo.sh no-network    # config C: application only, network feature off
./scripts/beyla-demo.sh nodes         # config D: config B plus k8s.src.node.name and k8s.dst.node.name
./scripts/beyla-demo.sh default       # back to config A
./scripts/beyla-demo.sh port-forward  # Prometheus on localhost:39090
./scripts/beyla-demo.sh down
```

`make beyla-demo` is `up`. `BEYLA_DEMO_NODES=2 ./scripts/beyla-demo.sh up` builds a
control plane and a worker instead, with `client` pinned to the control plane and
`web`/`db` to the worker, so their flows cross nodes. Use it with config D.

## The three configurations, and why all three matter

| | Features | `attributes.select` | What it represents |
|---|---|---|---|
| **A** (`default`) | network + application | none | An operator turned network metrics on and changed nothing else. **This is the realistic default.** |
| **B** (`attrs`) | network + application | `dst.port`, `transport`, `direction` | What a per-port traffic map actually needs. |
| **C** (`no-network`) | application only | none | Stock Beyla. The network flow metric does not exist. |
| **D** (`nodes`) | network + application | B plus `k8s.src.node.name`, `k8s.dst.node.name` | What counting a cross-node flow once needs. Two-node cluster only. |

Test against A before B. A is what users will have.

## What this cluster establishes

Read these before changing the Beyla source — each one is a live observation, not
an inference from Beyla's code.

**`dst_port` and `transport` are not exported by default.** Both are
`Default: false` in the attribute registry, so configuration A emits neither. A
consumer grouping by them gets empty strings, which is how a port becomes `0` and
how UDP becomes TCP. Configuration B is the only one where they appear.
In configuration A Radar fills a port-0 edge from the destination's Services
(their target port, marked "from Service"; "multiple (from Service)" when they
declare several), so a port shown there is declared, not observed.

**`direction` is exported by default, and every conversation is reported twice.**
Values are `request`, `response` and `unknown`. The response series has source and
destination swapped, so a consumer that ignores the label draws a mirror edge for
every real edge.

UDP — DNS here — reports `unknown`, and it does so on **both** ends of the
conversation. That leaves no way to tell which side initiated it, so excluding only
`response` keeps a mirrored pair for every UDP conversation, and with `dst.port`
selected the reverse half carries ephemeral ports: 287 spurious coredns edges out
of 289 flows, measured.

Radar keeps `direction="request"` for the edges it can orient, and reads the
`unknown` conversations separately, collapsing each pair into a single edge drawn
without an arrowhead. The traffic is real; only its direction is not known. That
also disposes of the cardinality problem, since the pair is grouped without
`dst_port` and the ephemeral ports fold together instead of multiplying.

**Selecting `dst.port` makes the mirror explode.** Response-direction series carry
the *client's* ephemeral port, so one client-to-server conversation became 2
forward series against 400 mirrors, and the cluster went from 24 series to 1543
within minutes. Restricting to the request direction is what makes `dst.port`
usable at all; the two are not independent choices.

**`k8s_*_owner_type` includes `Service`, and Service-routed conversations are
reported twice with byte-identical values** — once attributed to the workload,
once to the Service. A dedup key that ignores owner type collapses the pair
arbitrarily; one that includes it double-counts the traffic. Neither is right:
pick the workload attribution deliberately.

**The HTTP metric carries `server_port` and `server_address` by default.** So L7
data can be joined to a specific L4 port. Nothing needs to infer which of a
destination's ports serves HTTP.

**Both halves of a UDP conversation are reported, and neither can be called the
request.** That is why those edges are drawn without an arrowhead rather than
oriented arbitrarily or dropped. A pair is also reported twice when a Service
fronts the destination, once per owner type with identical values, so anything
summing across attributions doubles it — see the note on `Service` above.

**With the network feature off, `beyla_build_info` survives.** That is the only
signal that separates "Beyla is installed but not watching the network" from
"Beyla is not installed", and the network feature being opt-in makes the former
the common case.

**Selecting `dst.port` makes received bytes badly wrong.** Measured here:
response-direction series carry the client's ephemeral port, so almost all of them
live for less than the gap between two scrapes and `rate()` returns nothing for
them. 2944 response series existed and 269 produced a rate — **91% of the bytes
dropped inside `rate()`**, before any grouping a consumer could change. The
`client -> web` edge then reported 51 bytes received per response, less than a bare
TCP ACK. The request direction is unaffected, because its series are keyed by the
server's stable port. So the two configurations trade off against each other: A has
no port detail but truthful byte counts, B has exact ports and a received-byte
figure that is roughly a tenth of the truth. Anything that recommends B for the
sake of per-port edges should say what it costs.

**A flow between two nodes is reported by both nodes' agents.** With two nodes,
`client -> web` arrives as two series with identical flow labels, one per agent,
so a consumer summing them doubles the bytes. The copies differ only in the
scrape's own target labels: `instance`, plus the `node` label this Prometheus
adds by relabelling. Radar drops `instance`, `pod`, `node` and `host` before
collapsing the copies; any other per-agent target label would keep them apart and
the flow would still count twice. Without the node attributes (configs A-C), the copies cannot
be told apart from two replicas of one owner running on different nodes. The
Service-attributed series has no destination node and comes from one agent only.

## Metric names

Grafana's Beyla emits `beyla_network_flow_bytes_total`; upstream OpenTelemetry
eBPF Instrumentation (OBI), which Beyla vendors and renames back, emits
`obi_network_flow_bytes_total`. Both distributions are current. This cluster runs
Grafana's, so only the `beyla_` name appears here — the `obi_` path is not
exercised.

## Fixtures

Namespace `demo`:

- `client` — busybox loop making HTTP requests to `web:80` and TCP connections to
  `db`, plus DNS lookups. The DNS traffic is the only UDP in the cluster and the
  only source of `direction="unknown"`.
- `worker` — a second, much quieter caller, reaching `web:8080`. It exists because
  the HTTP metric carries no caller labels, so a destination's request rate has to
  be divided between the callers that reached it. With one caller that division is
  the identity and proves nothing; the volumes are lopsided on purpose so a rate
  copied to both callers is obvious rather than plausible.
- `web` — nginx on **:80 and :8080**, **two replicas**. Serves the HTTP metric,
  including `server_port`. Each port has a failing route (`/boom` on :80, `/bad` on
  :8080) alongside a 200, so the 5xx path has something to report — an error rate of
  zero proves nothing about whether the code reads it. Two ports make the
  destination genuinely multi-port rather than multi-port only by way of ephemeral
  client ports, and two replicas make it report every L7 figure twice, since those
  metrics are per pod.
- `db` — redis on :6379. Non-HTTP, so it exercises a destination with no L7 data.

Both `web` and `db` are fronted by Services, which is what produces the duplicate
owner-type series.

## Limits

Single node by default, so node-spanning flows and two agents reporting the same
conversation are exercised only with `BEYLA_DEMO_NODES=2`. No SCTP or ICMP, so non-TCP/UDP transport handling
is untested. Upstream OBI is not installed, and neither is Alloy, so the
Alloy-detection path is not exercised here.

kind nodes do not mount bpffs, so Beyla logs a warning about pinned maps being
unavailable. Network and HTTP metrics are unaffected; the warning is expected.
