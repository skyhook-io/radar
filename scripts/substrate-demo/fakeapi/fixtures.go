package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/skyhook-io/radar/internal/substrate/ateapi"
)

const (
	busyboxImage   = "busybox:1.36.1"
	scriptsCM      = "substrate-demo-worker-logs"
	tsSentinel     = "@@TS@@"
	drainAnno      = "substrate-demo.radar.skyhook.io/drain"
	notReadyAnno   = "substrate-demo.radar.skyhook.io/not-ready"
	scriptHashAnno = "substrate-demo.radar.skyhook.io/script-hash"
	// drainGrace keeps the draining worker's Pod Terminating (and logging)
	// for a day: sh as PID 1 survives SIGTERM, so only the grace period ends it.
	drainGrace = 86400
)

func actorLabels(r *actorRec, container string) map[string]string {
	a := r.actor
	l := map[string]string{
		"ate.atespace":          a.Metadata.Atespace,
		"ate.actor.name":        a.Metadata.Name,
		"ate.actor.uid":         a.Metadata.Uid,
		"ate.template.atespace": a.ActorTemplate.Atespace,
		"ate.template.name":     a.ActorTemplate.Name,
	}
	if container != "" {
		l["ate.actor.container.name"] = container
	}
	return l
}

type line struct {
	age    time.Duration
	prefix string
	suffix string
}

func mkLine(age time.Duration, m map[string]any) line {
	m["time"] = tsSentinel
	b, _ := json.Marshal(m)
	pre, suf, _ := strings.Cut(string(b), `"`+tsSentinel+`"`)
	return line{age: age, prefix: pre, suffix: suf}
}

func lifecycle(age time.Duration, r *actorRec, msg string) line {
	m := map[string]any{"message": msg, "labels": actorLabels(r, "")}
	if len(msg)%2 == 0 {
		m["trace_id"] = hexOf(32, "trace", r.actor.Metadata.Uid, msg)
		m["span_id"] = hexOf(16, "span", r.actor.Metadata.Uid, msg)
		m["trace_flags"] = "01"
	}
	return mkLine(age, m)
}

// container mirrors actorlog.WrapContainerLogs: a JSON object line keeps its
// fields and gains the labels; anything else becomes the message.
func container(age time.Duration, r *actorRec, cont string, out any) line {
	switch v := out.(type) {
	case map[string]any:
		m := map[string]any{}
		for k, val := range v {
			m[k] = val
		}
		m["labels"] = actorLabels(r, cont)
		return mkLine(age, m)
	default:
		return mkLine(age, map[string]any{"message": fmt.Sprint(v), "labels": actorLabels(r, cont)})
	}
}

func workerLog(age time.Duration, level, msg string, kv ...any) line {
	m := map[string]any{"level": level, "msg": msg}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return mkLine(age, m)
}

// usageLine is the worker's own slog record for the usage event: identity
// keys are flat, not under labels, so kubectl-ate does not attribute it.
func usageLine(age time.Duration, r *actorRec, p *workerPlan, usage int64, epoch int64) line {
	a := r.actor
	return mkLine(age, map[string]any{
		"level": "INFO", "msg": "Actor usage sampled",
		"ate.atespace": a.Metadata.Atespace, "ate.actor.name": a.Metadata.Name, "ate.actor.uid": a.Metadata.Uid,
		"ate.template.atespace": a.ActorTemplate.Atespace, "ate.template.name": a.ActorTemplate.Name,
		"ate.workerpool.namespace": fixtureNamespace, "ate.workerpool.name": p.pool,
		"ate.sandbox.class": r.tmpl.class, "ate.stats.source": "cgroup", "ate.stats.kind": "periodic",
		"ate.actor.epoch": epoch, "ate.stats.memory.usage": usage,
	})
}

func outputFor(r *actorRec, i int) (string, any) {
	n := r.actor.Metadata.Name
	switch r.tmpl.key {
	case tADK:
		ticket := strings.TrimPrefix(n, "ticket-")
		return "agent", []map[string]any{
			{"severity": "INFO", "message": "Classified ticket", "ticket": ticket, "intent": "billing_question", "confidence": 0.91},
			{"severity": "INFO", "message": "Tool call", "tool": "zendesk.get_ticket", "latency_ms": 182},
			{"severity": "INFO", "message": "Drafted reply", "ticket": ticket, "tokens": 412},
			{"severity": "WARNING", "message": "Rate limited by upstream, retrying", "retry_in_s": 2},
		}[i%4]
	case tClaude:
		return "agent", []string{
			"Fetching PR diff (14 files, +412 -96)",
			"Running: go test ./internal/server/... (exit 0)",
			"Reviewing internal/server/handlers.go",
			"Posted 3 review comments",
		}[i%4]
	case tLang:
		return "agent", []map[string]any{
			{"level": "info", "msg": "node completed", "node": "retrieve", "docs": 12},
			{"level": "info", "msg": "node completed", "node": "summarize", "tokens": 2391},
			{"level": "info", "msg": "node started", "node": "critique"},
		}[i%3]
	case tCodex:
		return "codex", []string{"exec: pytest -q tests/unit (41 passed)", "applying patch to src/parser.py", "exec: ruff check . (0 issues)"}[i%3]
	case tBrowser:
		if i%2 == 0 {
			return "browser", "[chromium] navigated to https://docs.example.com/start (200)"
		}
		return "mcp-server", map[string]any{"level": "info", "msg": "tools/call", "tool": "browser_navigate"}
	case tNotebook:
		return "kernel", fmt.Sprintf("Executing cell %d/40", 10+i%30)
	}
	return "agent", "working"
}

func hasSnapshot(r *actorRec) bool { return len(r.actor.Status.Snapshots) > 0 }

// podLog is what a worker Pod prints: when it starts, the lifecycle events of
// the actors it holds now, then those actors' output. A fixture Pod is new, so
// it holds nothing older: no line is backdated, and actors that ran on
// another worker, crashed, or ran under an earlier worker process print
// nothing here.
func podLog(p *workerPlan) (start, periodic []line) {
	recs := append([]*actorRec(nil), p.actors...)
	sort.Slice(recs, func(i, j int) bool { return recs[i].actor.Metadata.Uid < recs[j].actor.Metadata.Uid })
	if p.registered > 1 {
		start = append(start, workerLog(0, "INFO", "Worker process registered", "epoch", p.registered))
	}
	for idx, r := range recs {
		if r.plan != p || (r.sp != nil && r.sp.staleEpoch) {
			continue
		}
		boot := func(finished bool) {
			begin, done := "Actor starting", "Actor started"
			if hasSnapshot(r) {
				begin, done = "Actor restoring", "Actor restored"
			}
			start = append(start, lifecycle(0, r, begin))
			if !finished {
				return
			}
			if r.tmpl.class == "gvisor" {
				start = append(start, container(0, r, "_sandbox", "runsc: sandbox started"))
			}
			start = append(start, lifecycle(0, r, done))
		}
		switch r.actor.Status.State {
		case ateapi.ActorState_ACTOR_STATE_RUNNING:
			boot(true)
			for i := 0; i < 2; i++ {
				c, o := outputFor(r, idx+i)
				periodic = append(periodic, container(0, r, c, o))
			}
			if idx%4 == 0 {
				periodic = append(periodic, usageLine(0, r, p, r.tmpl.mem*6/10, r.actor.Status.GetWorkerAssignment().GetWorkerEpoch()))
			}
		case ateapi.ActorState_ACTOR_STATE_RESUMING:
			boot(false)
		case ateapi.ActorState_ACTOR_STATE_SUSPENDING:
			boot(true)
			c, o := outputFor(r, idx)
			start = append(start, container(0, r, c, o), lifecycle(0, r, "Actor checkpointing"))
		}
	}
	return start, periodic
}

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// PodScript is the busybox sh program a fixture worker Pod runs.
func PodScript(p *workerPlan) string {
	start, periodic := podLog(p)
	var b strings.Builder
	b.WriteString("#!/bin/sh\n# Generated by scripts/substrate-demo/fakeapi; edits are overwritten by `substrate-demo.sh up`.\n")
	b.WriteString(`now() { date -u +%Y-%m-%dT%H:%M:%SZ; }` + "\n")
	b.WriteString(`p() { printf '%s"%s"%s\n' "$1" "$2" "$3"; }` + "\n")
	if p.state == ateapi.WorkerState_WORKER_STATE_DRAINING {
		term := workerLog(0, "INFO", "Received SIGTERM; draining, waiting for hosted actors to be suspended")
		fmt.Fprintf(&b, "onterm() { p %s \"$(now)\" %s; }\ntrap onterm TERM\n", shq(term.prefix), shq(term.suffix))
	}
	for _, l := range start {
		fmt.Fprintf(&b, "p %s \"$(now)\" %s\n", shq(l.prefix), shq(l.suffix))
	}
	b.WriteString("i=0\nwhile :; do\n  t=$(now)\n")
	if len(periodic) > 0 {
		fmt.Fprintf(&b, "  case $((i %% %d)) in\n", len(periodic))
		for i, l := range periodic {
			fmt.Fprintf(&b, "  %d) p %s \"$t\" %s ;;\n", i, shq(l.prefix), shq(l.suffix))
		}
		b.WriteString("  esac\n")
	}
	b.WriteString("  i=$((i + 1))\n  sleep 4\ndone\n")
	return b.String()
}

func toYAML(objs ...any) (string, error) {
	var parts []string
	for _, o := range objs {
		y, err := yaml.Marshal(o)
		if err != nil {
			return "", err
		}
		parts = append(parts, string(y))
	}
	return strings.Join(parts, "---\n"), nil
}

func fixtureLabels(extra map[string]string) map[string]string {
	l := map[string]string{fixtureLabel: fixtureValue}
	for k, v := range extra {
		l[k] = v
	}
	return l
}

// Manifests returns every Kubernetes object the fixture owns.
func Manifests(d *Dataset) (string, error) {
	scripts := map[string]string{}
	var objs []any
	for _, p := range d.plans {
		if p.owned {
			scripts[p.pod+".sh"] = PodScript(p)
		}
	}
	objs = append(objs, map[string]any{
		"apiVersion": "v1", "kind": "Namespace",
		"metadata": map[string]any{"name": fixtureNamespace, "labels": fixtureLabels(nil)},
	})
	objs = append(objs, map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{"name": scriptsCM, "namespace": fixtureNamespace, "labels": fixtureLabels(nil)},
		"data":     scripts,
	})
	objs = append(objs,
		sandboxConfig("gvisor-default", "gvisor", "v1", map[string]any{"gvisor": asset("gvisor")}),
		sandboxConfig("microvm", "microvm", "v2", map[string]any{
			"cloud-hypervisor": asset("cloud-hypervisor"), "virtiofsd": asset("virtiofsd"),
			"kata-kernel": asset("kata-kernel"), "guest-rootfs": asset("guest-rootfs"),
		}),
	)
	for _, ps := range poolSpecs {
		objs = append(objs, workerPool(ps))
	}
	for _, p := range d.plans {
		if !p.owned {
			continue
		}
		grace := 2
		annos := map[string]string{scriptHashAnno: podHash(p)}
		if p.state == ateapi.WorkerState_WORKER_STATE_DRAINING {
			grace = drainGrace
			annos[drainAnno] = "true"
		}
		worker := map[string]any{
			"name": "worker", "image": busyboxImage,
			"command": []string{"sh", "/scripts/" + p.pod + ".sh"},
			"resources": map[string]any{
				"requests": map[string]string{"cpu": "2m", "memory": "8Mi"},
				"limits":   map[string]string{"memory": "32Mi"},
			},
			"volumeMounts": []any{map[string]any{"name": "scripts", "mountPath": "/scripts", "readOnly": true}},
		}
		if podNotReady(p) {
			worker["readinessProbe"] = notReadyProbe
			annos[notReadyAnno] = "true"
		}
		objs = append(objs, map[string]any{
			"apiVersion": "v1", "kind": "Pod",
			"metadata": map[string]any{
				"name": p.pod, "namespace": fixtureNamespace,
				"labels":      fixtureLabels(map[string]string{"ate.dev/worker-pool": p.pool}),
				"annotations": annos,
			},
			"spec": map[string]any{
				"terminationGracePeriodSeconds": grace,
				"automountServiceAccountToken":  false,
				"enableServiceLinks":            false,
				"containers":                    []any{worker},
				"volumes":                       []any{map[string]any{"name": "scripts", "configMap": map[string]any{"name": scriptsCM}}},
			},
		})
	}
	return toYAML(objs...)
}

func scriptHash(script string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(script)))[:16]
}

func asset(name string) map[string]string {
	sum := hexOf(64, "asset", name)
	return map[string]string{"url": "s3://substrate-assets-demo/microvm-assets/amd64/" + sum, "sha256": sum}
}

func sandboxConfig(name, class, version string, assets map[string]any) map[string]any {
	return map[string]any{
		"apiVersion": "ate.dev/v1", "kind": "SandboxConfig",
		"metadata": map[string]any{
			"name": name, "labels": fixtureLabels(nil),
			"annotations": map[string]string{"sandboxconfig.ate.dev/is-class-default": "true"},
		},
		"spec": map[string]any{
			"sandboxClass": class, "defaultVersion": version,
			"versions": []any{map[string]any{"name": version, "state": "Enabled", "assets": map[string]any{"amd64": assets}}},
		},
	}
}

func workerPool(ps poolSpec) map[string]any {
	return map[string]any{
		"apiVersion": "ate.dev/v1", "kind": "WorkerPool",
		"metadata": map[string]any{"name": ps.name, "namespace": fixtureNamespace, "labels": ps.labels},
		"spec": map[string]any{
			"replicas":       ps.desired,
			"sandboxClasses": []any{map[string]any{"name": ps.class, "configRef": map[string]string{"name": ps.config}}},
			"workerImage":    "example.invalid/ateworker-" + ps.class + ":v1",
		},
	}
}

// PoolStatusPatches is the status each pool reports; no controller runs, so
// it is written to agree with the worker Pods: replicas counts them, ready
// those Kubernetes calls Ready (all but the unavailable worker's, whose
// readiness probe fails; a Terminating Pod stays Ready while it runs).
func PoolStatusPatches(d *Dataset) map[string]string {
	out := map[string]string{}
	now := time.Now().UTC().Format(time.RFC3339)
	for _, ps := range poolSpecs {
		replicas, ready := 0, 0
		for _, p := range d.plans {
			if p.pool != ps.name {
				continue
			}
			replicas++
			if !podNotReady(p) {
				ready++
			}
		}
		b, _ := json.Marshal(map[string]any{"status": map[string]any{
			"replicas": replicas, "readyReplicas": ready, "selector": "ate.dev/worker-pool=" + ps.name,
			"sandboxClasses": []any{map[string]any{
				"name": ps.class, "configRef": map[string]string{"name": ps.config},
				"conditions": []any{map[string]any{"type": "Resolved", "status": "True", "reason": "Resolved", "message": "", "lastTransitionTime": now}},
			}},
		}})
		out[ps.name] = string(b)
	}
	return out
}

// ReadPods parses `kubectl get pods -o json`.
func ReadPods(path string) (map[string]PodInfo, error) {
	if path == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name                       string     `json:"name"`
				UID                        string     `json:"uid"`
				CreationTimestamp          time.Time  `json:"creationTimestamp"`
				DeletionTimestamp          *time.Time `json:"deletionTimestamp"`
				DeletionGracePeriodSeconds *int64     `json:"deletionGracePeriodSeconds"`
			} `json:"metadata"`
			Spec struct {
				NodeName string `json:"nodeName"`
			} `json:"spec"`
			Status struct {
				PodIP string `json:"podIP"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	out := map[string]PodInfo{}
	for _, it := range list.Items {
		pi := PodInfo{
			Name: it.Metadata.Name, UID: it.Metadata.UID, Node: it.Spec.NodeName, IP: it.Status.PodIP,
			Terminating: it.Metadata.DeletionTimestamp != nil, Found: true, Created: it.Metadata.CreationTimestamp,
		}
		if del, grace := it.Metadata.DeletionTimestamp, it.Metadata.DeletionGracePeriodSeconds; del != nil && grace != nil {
			pi.DrainStart = del.Add(-time.Duration(*grace) * time.Second)
		}
		out[it.Metadata.Name] = pi
	}
	return out, nil
}

// The unavailable worker's Pod runs and logs but never turns Ready: Substrate
// marks a worker UNAVAILABLE while its Pod is not Ready.
var notReadyProbe = map[string]any{"exec": map[string]any{"command": []string{"false"}}, "periodSeconds": 10}

func podNotReady(p *workerPlan) bool { return p.state == ateapi.WorkerState_WORKER_STATE_UNAVAILABLE }

// podHash changes whenever the Pod must be recreated: its script, or a spec
// field Kubernetes does not let an update change.
func podHash(p *workerPlan) string {
	h := PodScript(p)
	if podNotReady(p) {
		h += "\x00notReady"
	}
	return scriptHash(h)
}
