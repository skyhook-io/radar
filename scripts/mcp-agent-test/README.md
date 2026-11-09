# MCP agent test

Unit tests prove a tool does what its code says. They can't prove that an agent reading the tool's
description picks the right tool, passes the right arguments and recovers from an error. This
test does: it gives real agents (Claude Code and Codex) only Radar's MCP, with no shell and no
kubeconfig, asks them to do real tasks on a real cluster, and then checks the cluster itself
instead of trusting the agent's summary.

Run it after you change what agents read: a tool's description or parameters, the server
instructions (`pkg/investigation/guidance.go`), parameter repair (`internal/mcp/toolparams.go`),
or the wording of errors and results. It isn't part of CI: it costs model tokens, needs a cluster,
and its results vary from run to run.

> [!WARNING]
> Agents act through Radar with the identity of the kubeconfig context you choose. Use a test
> cluster. The runner creates one namespace per agent, labelled `radar-agent-test=<run id>`, and
> deletes them at the end unless you pass `--keep`.

## Run

Requirements: `go` (unless you pass `--radar-bin`), `kubectl`, `helm`, and the agent CLIs you pick
(`claude`, `codex`), logged in. The cluster needs to reach the suite's chart sources.

```bash
# Every Helm task with the default agents, against a test context
scripts/mcp-agent-test/run.py --context my-test-cluster

# A quick check: two tasks, one agent
scripts/mcp-agent-test/run.py --context my-test-cluster --agents codex:gpt-6.1-sol --tasks P0,A1

# Claude Code with tools loaded on demand (its default), next to every tool up front
scripts/mcp-agent-test/run.py --agents claude-deferred:claude-sonnet-5-5,claude:claude-sonnet-5-5

# Rebuild the report of an earlier run
scripts/mcp-agent-test/run.py --report /path/to/run
```

The runner builds Radar from this checkout (backend only), starts it on a free port with its own
home directory and Helm configuration so your settings and repositories stay out, and runs the
agents in parallel. The kubeconfig it hands Radar is a flattened copy of the one context, kept in a
private temporary directory that is removed however the run ends. Each agent works through the tasks in order, one session per task, and later tasks build
on earlier ones: a subset has to keep its prerequisites (`A1,A11`, not `A11` alone). Agent kinds:

| Kind | What the agent sees |
|---|---|
| `claude` | `claude -p` with no built-in tools, so every Radar tool loads up front |
| `claude-deferred` | `claude -p` with tool search, so Radar's tools load on demand by name, as in default Claude Code |
| `codex` | `codex exec` with its shell, apps, browsers and plugins turned off, and Radar's tools pre-approved. Its code-mode host stays on: Codex makes MCP calls through it |

The model ids in the default `--agents` go stale; pass current ones.

## Read the results

The run directory (printed at the start and end) holds, per agent, each task's transcript
(`<task>.jsonl`) and the release state after it (`<task>.state.json`), plus `radar.log`,
`results.jsonl` and `report.md`. The report lists every tool call with its arguments, each error
Radar returned, and the agent's reply. Each task gets a verdict:

- **PASS / FAIL**: the suite's check decided from the cluster state.
- **READ**: the state can't decide (a read-only task, or a refusal that may be correct). Read the
  reply against the suite's answer key, and check the calls marked `(write tool)`: the state check
  covers Helm releases, not every object an agent could change.
- **ERROR**: the agent CLI failed or timed out, the agent made no Radar call at all (it couldn't
  reach the MCP), or the runner failed. That agent's later tasks don't run, since they build on a
  state nobody checked.

A call marked `NOT RADAR` used something other than Radar's MCP, which the setup is meant to rule
out: treat it as a broken run, not a result.

A pass with errors is worth reading too: it means an agent had to recover, and the error text or
the description it misread is usually what to fix.

## Suites

Each suite is a folder under `suites/` with a `suite.py` and an answer key. `suite.py` defines:

- `TASKS`: task id to prompt, with `{ns}` for the agent's namespace, in order.
- `prepare(env)`: configure what Radar needs before it starts (for example Helm repositories), and
  return Radar's `settings.json`.
- `setup(env, ns)`: create the starting fixtures in one agent's namespace.
- `state(env, ns)`: the facts the checks read, taken from the cluster.
- `check(task, before, after)`: `(True | False | None, note)`, where `None` means READ.
- Optional `ready(base, wait_http)`: refuse to start when Radar lacks something the suite needs,
  and `RADAR_FLAGS`: extra flags for Radar.

`env` runs `helm` and `kubectl` against the test context, never the agents' configuration.

| Suite | Covers |
|---|---|
| `helm` | `manage_helm` install, values edits, upgrade with preview, rollback, uninstall; `get_helm_release` and `get_helm_chart` reads. [Answer key](suites/helm/SCENARIOS.md) |

## Last results

2026-11-09, on `main` after #2543, against an EKS test cluster with the Helm 4.2.3 CLI:

| Agent | Result |
|---|---|
| Claude Opus 5.5 | 13/13, no tool errors |
| Claude Sonnet 5.5 | 13/13, no tool errors |
| Claude Sonnet 5.5, tools on demand | 13/13, no tool errors |
| GPT 6.1 (Codex) | 13/13, no tool errors |

P0, A3 and A8 are READ tasks; every reply was read and matched the answer key (A3's only write-tool
calls were dry-run upgrades). The Claude agents ran together and the Codex agent ran alone.

Known limit: when four agents install from Bitnami at the same moment, A10 can fail with source
timeouts. Bitnami's index is about 28 MB and it throttles parallel downloads. In an earlier run
that day all four installs were refused before anything was applied, and every agent said why.
