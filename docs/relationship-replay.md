# Relationship replay

Capture once, run two testserver builds over that file, and compare their HTTP
and MCP relationship readers. Replay never loads kubeconfig or contacts a
cluster. The capture command is the only command below that uses a real cluster.

## Capture (operator-run)

```sh
go run ./cmd/clustercapture -context YOUR_CONTEXT -out /tmp/cluster.ndjson.gz
```

Discovery covers every served group/version, including CRDs. Lists use a limit
of 500 across all namespaces. Events, coordination leases and subresources are
excluded. A discovery or list error fails the capture; an incomplete capture is
never published. The destination must not already exist. Capture uses list and
discovery reads only, with a 30-second timeout per network request.

The gzip contains a header with `captured_at`, SHA-256 `context_hash`, and
`discovery.groups` / `discovery.resources`, followed by Kubernetes objects.
Every version is retained. Subject comparison deduplicates versions by
(group, kind, namespace, name); empty discovered kinds are still listed.

This is a frozen **recording**, not an atomic cross-resource apiserver snapshot:
objects may change while capture lists different kinds/versions. Both replays
nevertheless receive exactly the same recorded inputs.

## Build old and new with the same harness

Unmodified main has no `-replay` flag. Apply this harness to both code revisions;
do not copy the new relationship implementation onto the old side. For example,
from the harness worktree while changes are uncommitted:

```sh
HARNESS_ROOT=$PWD
git diff HEAD -- cmd/testserver/main.go internal/k8s/testing.go > /tmp/replay-harness.patch
git worktree add --detach /tmp/radar-old origin/main
git worktree add --detach /tmp/radar-new YOUR_NEW_REVISION
for tree in /tmp/radar-old /tmp/radar-new; do
  git -C "$tree" apply /tmp/replay-harness.patch
  cp "$HARNESS_ROOT/cmd/testserver/replay.go" "$tree/cmd/testserver/"
  cp -R "$HARNESS_ROOT/internal/clusterreplay" "$tree/internal/"
done
(cd /tmp/radar-old && go build -o /tmp/radar-old-testserver ./cmd/testserver)
(cd /tmp/radar-new && go build -o /tmp/radar-new-testserver ./cmd/testserver)
go build -o /tmp/reldiff ./cmd/reldiff
```

Resolve overlay conflicts manually, preserving each revision's product code.
The overlay touches only testserver, the test hook, and the capture-file package.
If the harness has been committed, generate the patch against its pre-harness
base instead of `HEAD`. Never remove worktrees through symlink paths.

## Replay and compare

```sh
/tmp/radar-old-testserver -replay /tmp/cluster.ndjson.gz -port 9381 -admin-port 9382 > /tmp/radar-old.log 2>&1 &
OLD_PID=$!
/tmp/radar-new-testserver -replay /tmp/cluster.ndjson.gz -port 9383 -admin-port 9384 > /tmp/radar-new.log 2>&1 &
NEW_PID=$!
/tmp/reldiff -a http://127.0.0.1:9381 -b http://127.0.0.1:9383 \
  -capture /tmp/cluster.ndjson.gz -out /tmp/relationship-diff.json
kill "$OLD_PID" "$NEW_PID"
```

Readiness is `GET /loadtest/status` on the admin listener (default main port+1).
Reldiff polls it for up to 30 seconds. Override custom admin locations with
`-ready-a URL -ready-b URL`. Readiness includes unsupported resources. All
watchable captured kinds are eagerly synced so request order does not determine
ownership coverage. Non-watchable kinds remain in discovery/the fake tracker,
and are explicitly reported as unsupported by the informer-backed readers.

Each subject exercises resource GET `relationships`, REST
`/api/ai/neighborhood`, REST AI resource context, and MCP `get_resource` /
`get_neighborhood` through JSON-RPC `tools/call`. There is no separate non-AI
REST neighborhood route in today's server. MCP is mounted read-only at `/mcp`.
Reldiff also reads `/api/applications`, each kind's `include=summary` list, and
every captured Argo Application / Flux Kustomization / HelmRelease GitOps tree.
`-max-per-kind N` caps subject readers per group/kind; trees and list readers
remain exhaustive. Group query parameters disambiguate colliding kind names.

The JSON groups deltas by reader, JSON-pointer field and subject group/kind,
with full counts and at most five examples. Reference/object sets are sorted;
ordered scalar arrays remain ordered. Normalization drops the explicit volatile
key set in `cmd/reldiff/main.go` (timestamps, ages, durations, request IDs and
generation/resource-version counters). Other content is retained, including
errors. Failures are recorded independently on both sides; **zero deltas with
failed calls is not full coverage**. Inspect `failures` and readiness's
`unsupported` arrays before claiming parity. A missing endpoint is a failed
reader call, not a silently skipped reader.

Latency is one timed call per reader/subject/side, reported as median/p95 by
reader and group/kind, including errors. Side order alternates to distribute
cache/load bias. Small captures do not support meaningful p95 claims. This is
an inexpensive comparison, not an isolated benchmark. Successful report writing
returns exit zero even when deltas or reader failures exist; inspect the report.
Responses over 64 MiB fail explicitly.

## Local fixture

```sh
gzip -c internal/clusterreplay/testdata/tiny.ndjson > /tmp/tiny.ndjson.gz
go build -o /tmp/replay-testserver ./cmd/testserver
# Use the replay/diff commands above with this binary on both sides.
```

The fixture has a Deployment → ReplicaSet → Pod, Service, Node, Namespace,
Widget CRD, two Widgets and an Argo Application. To demonstrate a mismatch,
change the Service selector from `app: demo` to `app: unmatched` in a second
capture and restart only the second server with that file.

## Privacy and limits

Secret `data`/`stringData` and ConfigMap `data`/`binaryData` values are replaced
with empty strings, preserving their keys. Managed fields and last-applied
annotations are removed recursively. Literal env values are cleared; token,
password, authorization and private-key values are redacted through Radar's
existing inline-secret redactor, which also handles recognized credential patterns
in other strings. Kubeconfig contents, credential objects and the plaintext context name
are never serialized. Files are created with mode 0600.

Arbitrary CRDs and free-form strings can encode credentials under unknown names
or encodings; no heuristic sanitizer can promise their absence. Treat captures
and reports as private local artifacts and review them before sharing. This is
a remaining limitation of the absolute “never write tokens” requirement.
Redaction deliberately changes data-dependent relationships (for example TLS
certificate parsing or Helm release payloads); replay compares the sanitized
world, not unavailable secret contents. Fake clients do not perform version
conversion, admission, aggregated-API behavior or live controller reconciliation.
Multiple recorded versions can disagree; the typed fake uses the first known
version, while the dynamic fake retains versions independently. Unsupported
resources and reader failures remain visible rather than being dropped.

## Separating run-to-run noise from real differences

Some readers vary between two runs of the *same* build (today: the GitOps tree and
neighborhood around CiliumEndpoints, and the order Applications joins its add-on
reasons). Measure that first and pass it as a baseline, so a comparison of two
builds reports those fields as `noise`, not as deltas:

```sh
# two servers from the same build, then
reldiff -a http://127.0.0.1:PORT_A -b http://127.0.0.1:PORT_B -capture c.ndjson.gz -out same-build.json
# old build on A, new build on B
reldiff -a ... -b ... -capture c.ndjson.gz -baseline same-build.json -out old-vs-new.json
```

Arrays of resource refs, listed objects and graph nodes/edges are compared by identity,
not position, so a reordered list is not a delta and a changed element is reported
under its own key.

A capture skips a resource whose list fails and writes the skipped resources next to
the capture (`<out>.skipped.json`). Groups discovery couldn't answer for are recorded in
the capture header (`failed_groups`); the metrics groups are never captured, since their
values change on every read.
