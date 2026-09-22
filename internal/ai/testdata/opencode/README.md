# OpenCode stream fixtures

Captured from OpenCode 1.18.5 (`opencode run --format json --auto`) against a
disposable kind cluster. Diagnosis, follow-up and Apply used AWS Bedrock Sonnet
4.6. The provider-error fixture is the no-login provider's HTTP 403 response.
The only cluster workload was `opencode-smoke/demo`, referencing the missing
`GREETING` key in ConfigMap `demo-config`. Apply added `GREETING: hello`.

The JSONL files are the unmodified CLI stdout; they contain no credentials.
Terminal tool events include `part.id`, `part.tool`, and `part.state` with
`status`, `input`, `output` (or `error`). The CLI emits no running tool event.

Producer contract:
- https://github.com/anomalyco/opencode/blob/v1.18.5/packages/opencode/src/cli/cmd/run.ts
- https://github.com/anomalyco/opencode/blob/v1.18.5/packages/opencode/src/mcp/catalog.ts

Error, truncation, and private-provenance edge cases use the same event shape in
the Go tests. Full-local Apply remains unconfirmed by design; current-state
verification uses authenticated read evidence.
