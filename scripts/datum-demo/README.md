# Datum / Milo fixture

This disposable fixture runs Milo v0.36.0 and etcd in `milo-system` on the explicitly allowed `radar-test-nonprod` context. It never selects a kubeconfig context or edits `~/.kube/config`. State, fixture certificates and the fixture admin token live in a private directory outside the repository. Use a separate port-forward on 16445 or above.

```sh
./scripts/datum-demo.sh up --state-dir /private/tmp/radar-datum-demo
kubectl --context radar-test-nonprod -n milo-system port-forward svc/milo-apiserver 16445:6443
./scripts/datum-demo.sh all --milo-kubeconfig /private/tmp/radar-datum-demo/milo.kubeconfig
./radar --kubeconfig /private/tmp/radar-datum-demo/milo.kubeconfig --no-browser --port 9345
```

With an already running Milo, skip `up` and supply its private kubeconfig to `install` and `seed`. `up` deploys only in the named context and namespace; `install` and `seed` refuse an ordinary Kubernetes API server. Milo must have ProjectControlPlane and DiscoveryContextFilter enabled. The root plane receives the `datum-demo` namespace and a fixture Organization and Project.

The CRDs are fetched from immutable upstream commits:

- dns-operator: `32aaaef43c996eb4740131194a213dc772d0f856`
- network-services-operator: `346077b6c854e196e15b782c175d1bb455005d96`
- compute: `7ce96d27d7e88b25707cba6fc0201f2e2e0892af`

Objects exercise compute Instance/Workload configuration and reconciling placement, EndpointSlice versus NetworkService backends, fresh/expired synthetic connector leases, verified domains, programmed DNS and proxies, rejected records, disconnected connectors, pending certificates and entirely unobserved status. All controller status patches are **synthetic**, marked `radar.skyhook.io/synthetic-status=true` and described as synthetic in condition messages. These patches verify Radar's interpretation and rendering of real schemas. They do not validate controller reconciliation, provider programming, public DNS, certificate issuance or traffic.

`example.test`, documentation IP addresses and synthetic origins are deliberate; no public hostname is provisioned. Do not infer reachability from a green controller condition. The fixture has no cloud provider or cells, PowerDNS or certificate issuer; installing CRDs alone does not supply those controllers or dependencies.

Re-running `seed` is idempotent. Do not delete the containing cluster or other namespaces as cleanup. Credentials and generated state are not committed.

## Project isolation

After seeding the root plane, derive a private kubeconfig with the server path
`/apis/resourcemanager.miloapis.com/v1alpha1/projects/radar-datum-demo/control-plane`
and seed that plane independently:

```sh
./scripts/datum-demo.sh seed --milo-kubeconfig /private/tmp/project.kubeconfig --project-plane
```

Root and project objects deliberately share names, exercising cache/history isolation.
The runtime connection action verifies the real Milo tenant API; it does not provision
or reconcile these objects.

## Time-boxed real controller startup attempts

Both published controller images were started locally against the project plane.
DNS v0.9.8 (`sha256:ac689aea47b63b8426d758cc9f06e2850bd1904e11172d51a9f4dd5a360ef453`)
exited because `PDNS_API_KEY or PDNS_API_KEY_FILE is required`.
Network services v0.31.1 (`sha256:d63a0c221da1e787211b84331c5a31a741dfb0c68864b30be62c471e66987123`)
exited because the NetworkInterfaceClaim API required by its indexer was unavailable.
Those image versions differ from the pinned schema commits above. No controller
reconciliation is claimed. A functioning provider, additional infrastructure APIs,
network cells/gateways and a certificate issuer would be needed for that validation.
