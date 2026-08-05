package server

import (
	"context"
	"hash/fnv"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/settings"
)

// cloudFunnelRolloutPercent stages the Cloud funnel's exposure. It is
// compiled in and ramped by releases (10 → higher → 100, then this file is
// deleted) — deliberately not a remote flag service, because Radar makes no
// network calls it doesn't announce and a rollout gate is not a reason to
// start.
const cloudFunnelRolloutPercent = 10

// cloudFunnelInCohort decides whether this installation sees the Cloud
// funnel during the staged rollout. RADAR_CLOUD_FUNNEL=on|off overrides in
// either direction (support, demos, opting a friendly cluster in early, or
// opting out permanently).
//
// Bucketing hashes a random install ID persisted in settings — local-only,
// never transmitted. In-cluster pods are excluded from the ramp instead of
// bucketed: without durable storage the bucket would re-roll on every
// restart and the funnel would flicker in and out; those installs join when
// the gate reaches 100 and is removed. The risky driver lane is local-only
// anyway, so the ramp cohort is exactly the population it needs feedback
// from.
func cloudFunnelInCohort() bool {
	switch strings.ToLower(os.Getenv("RADAR_CLOUD_FUNNEL")) {
	case "on", "1", "true":
		return true
	case "off", "0", "false":
		return false
	}
	return cloudFunnelBucketVerdict()
}

// Memoized: the bucket verdict cannot change within a process (the ID is
// sticky and the percent is compiled in), and capabilities are requested per
// page load — no reason to re-read the settings file each time. The env
// override above stays live deliberately.
var cloudFunnelBucketVerdict = sync.OnceValue(func() bool {
	return cloudFunnelBucketed(cloudFunnelRolloutPercent, rolloutIdentity())
})

// rolloutIdentity is the value the cohort hashes on. In-cluster it comes from
// the Radar Deployment's creation time, not ~/.radar: that directory sits on an
// emptyDir that dies with the pod, so bucketing on it would re-roll the verdict
// on every restart and rollout — and disagree between replicas — making the
// funnel flicker in and out.
//
// Empty either way means no durable identity, which cloudFunnelBucketed treats
// as out-of-cohort rather than re-rolling every start.
func rolloutIdentity() string {
	if k8s.IsInCluster() {
		if installed := k8s.InstalledAt(context.Background()); installed != 0 {
			return strconv.FormatInt(installed, 10)
		}
		return ""
	}
	return settings.InstallID()
}

func cloudFunnelBucketed(percent uint32, installID string) bool {
	if percent >= 100 {
		return true
	}
	// An empty ID means no persistable identity — stay out of a partial
	// rollout rather than re-rolling a fresh bucket every start.
	if percent == 0 || installID == "" {
		return false
	}
	h := fnv.New32a()
	h.Write([]byte(installID))
	return h.Sum32()%100 < percent
}
