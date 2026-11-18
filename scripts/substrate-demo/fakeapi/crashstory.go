package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/substrate/ateapi"
)

// A crashed actor whose worker Pod is still running has its last lines in
// that Pod's log: the run up to the crash. Pod logs are append-only, so those
// lines are written once per set of Pods, at real time, by `kubectl exec`
// into each Pod's stdout, and the crash times they record are kept in
// crashAnchorFile so every later API start serves the same crash times.
// Only crashes that happened after the Pod started get a story: a fixture
// Pod holds nothing from before it existed.

const crashAnchorFile = "crash-anchor"

// podSetKey identifies one incarnation of the Pods crashed actors last ran
// on, and the crashes as the dataset times them; recreating any other Pod
// leaves their lines, and so the key, alone. Retimed crashes get new lines,
// after the ones already in the log.
func podSetKey(pods map[string]PodInfo) string {
	last := map[string]bool{"batch-pool-worker-0": true} // the failed golden actor's
	var story []string
	for name, sp := range specials() {
		if sp.lastWorker != "" {
			last[sp.lastWorker] = true
		}
		if sp.crash != "" {
			story = append(story, fmt.Sprintf("%s|%s|%s|%s", name, sp.lastWorker, sp.crash, sp.crashAge))
		}
	}
	for _, t := range tmplSpecs {
		if t.golden == goldenFailed {
			story = append(story, fmt.Sprintf("%s|golden|%s", t.key, t.goldenAge))
		}
	}
	var uids []string
	for _, p := range defaultPlans() {
		if pi, ok := pods[p.pod]; ok && p.owned && pi.Found && last[p.pod] {
			uids = append(uids, p.pod+"="+pi.UID)
		}
	}
	sort.Strings(uids)
	sort.Strings(story)
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(uids, ",")+"\n"+strings.Join(story, ","))))[:16]
}

// ReadCrashAnchor returns the anchor recorded for this Pod set, or zero.
func ReadCrashAnchor(dir string, pods map[string]PodInfo) time.Time {
	b, err := os.ReadFile(filepath.Join(dir, crashAnchorFile))
	if err != nil {
		return time.Time{}
	}
	key, at, ok := strings.Cut(strings.TrimSpace(string(b)), "\n")
	if !ok || key != podSetKey(pods) {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return time.Time{}
	}
	return t
}

func writeCrashAnchor(dir string, pods map[string]PodInfo, at time.Time) error {
	return os.WriteFile(filepath.Join(dir, crashAnchorFile), []byte(podSetKey(pods)+"\n"+at.Format(time.RFC3339Nano)+"\n"), 0o644)
}

// crashStory reports whether r crashed on a worker whose Pod is still the one
// that ran it: the run it crashed in started after the Pod did.
func crashStory(r *actorRec, anchor time.Time) bool {
	if r.sp == nil || r.actor.Status.State != ateapi.ActorState_ACTOR_STATE_CRASHED || r.lastPlan == nil || !r.lastPlan.owned {
		return false
	}
	pi := r.lastPlan.podInfo
	if !pi.Found || pi.Created.IsZero() || pi.Terminating {
		return false
	}
	switch r.sp.crash {
	case msgOOM, msgWorkerRestart, msgRestoreFailure:
	default:
		return false
	}
	return anchor.Add(-r.sp.crashAge - 3*time.Minute).After(pi.Created)
}

// storyWait is how much older the worker Pods must get before every recent
// crash (within workerRestartAge: the incident and the worker restart) can
// have its lines. An anchor written sooner would leave some of them without
// lines for as long as these Pods live.
func (d *Dataset) storyWait(at time.Time) time.Duration {
	var wait time.Duration
	for _, r := range d.recs {
		if r.sp == nil || r.actor.Status.State != ateapi.ActorState_ACTOR_STATE_CRASHED || r.sp.crashAge > workerRestartAge || r.lastPlan == nil || !r.lastPlan.owned {
			continue
		}
		pi := r.lastPlan.podInfo
		if !pi.Found || pi.Created.IsZero() || pi.Terminating {
			continue
		}
		wait = max(wait, pi.Created.Add(r.sp.crashAge+3*time.Minute).Sub(at))
	}
	return wait
}

type timedLine struct {
	at   time.Time
	text string
}

func (l line) at(t time.Time) timedLine {
	return timedLine{at: t, text: l.prefix + `"` + t.UTC().Format(time.RFC3339Nano) + `"` + l.suffix}
}

// CrashLines are the lines each worker Pod's log holds for the actors that
// crashed on it, as of anchor, keyed by Pod name.
func (d *Dataset) CrashLines(anchor time.Time) map[string][]string {
	byPod := map[string][]timedLine{}
	idx := 0
	for _, r := range d.recs {
		if !crashStory(r, anchor) {
			continue
		}
		idx++
		p := r.lastPlan
		crash := anchor.Add(-r.sp.crashAge)
		run := crash.Add(-min(20*time.Minute, crash.Sub(p.podInfo.Created)/2))
		var ls []timedLine
		add := func(t time.Time, l line) { ls = append(ls, l.at(t)) }
		if r.sp.crash == msgRestoreFailure {
			add(crash.Add(-3*time.Second), lifecycle(0, r, "Actor restoring"))
			byPod[p.pod] = append(byPod[p.pod], ls...)
			continue
		}
		begin, done := "Actor starting", "Actor started"
		if len(r.actor.Status.Snapshots) > 0 {
			begin, done = "Actor restoring", "Actor restored"
		}
		add(run.Add(-2*time.Second), lifecycle(0, r, begin))
		add(run.Add(-time.Second), container(0, r, "_sandbox", "runsc: sandbox started"))
		add(run, lifecycle(0, r, done))
		span := crash.Sub(run)
		for i := 1; i <= 3; i++ {
			c, o := outputFor(r, idx+i)
			add(run.Add(span*time.Duration(i)/5), container(0, r, c, o))
		}
		switch r.sp.crash {
		case msgOOM:
			limit := r.tmpl.mem
			for i, frac := range []int64{80, 94, 99} {
				add(crash.Add(-time.Duration(3-i)*time.Minute), usageLine(0, r, p, limit*frac/100, p.registered))
			}
			add(crash.Add(-90*time.Second), container(0, r, "agent", map[string]any{"level": "warning", "msg": "context window near limit", "rss_bytes": limit * 97 / 100}))
			a := r.actor
			add(crash.Add(-time.Second), workerLog(0, "WARN", "Killing a sandbox over its memory limit",
				"ate.atespace", a.Metadata.Atespace, "ate.actor.name", a.Metadata.Name, "ate.actor.uid", a.Metadata.Uid,
				"ate.template.atespace", a.ActorTemplate.Atespace, "ate.template.name", a.ActorTemplate.Name,
				"memoryLimitBytes", limit, "ate.stats.memory.usage", limit))
			add(crash, workerLog(0, "WARN", "Sandbox died", "actorUID", a.Metadata.Uid, "pid", 4100+idx))
		case msgWorkerRestart:
			// The worker process died with the sandbox; nothing more is
			// written for the actor.
		}
		byPod[p.pod] = append(byPod[p.pod], ls...)
	}
	out := map[string][]string{}
	for pod, ls := range byPod {
		sort.SliceStable(ls, func(i, j int) bool { return ls[i].at.Before(ls[j].at) })
		for _, l := range ls {
			out[pod] = append(out[pod], l.text)
		}
	}
	return out
}
