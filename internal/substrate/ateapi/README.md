# Agent Substrate API (generated)

`ateapi.pb.go` and `ateapi_grpc.pb.go` are copied unchanged from
[agent-substrate/substrate](https://github.com/agent-substrate/substrate)
`api/ateapi/v1` at commit `d283af4ebaa9aba0bac571a827d3fbc954e2ec9a`
(Apache License 2.0, see the header of each file).

Radar copies them instead of importing `go.ate.dev/substrate`, whose module
requires a newer Go than Radar builds with. Substrate's v1 API is stable since
1.0 (its version-skew policy), so a refresh is only needed to read new fields.

To refresh: copy both files from the new commit, update the commit above, and
run `go build ./...`.
