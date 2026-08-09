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

// cloudFunnelRolloutPercent stages the Cloud funnel's exposure. It is compiled
// in and raised by successive releases until it reaches 100 and this file is
// deleted — deliberately not a remote flag service, because Radar makes no
// network calls it doesn't announce and a rollout gate is not a reason to
// start.
//
// Raising it only ever adds installs: the bucket is `hash % 100 < percent`, so
// anyone already inside a lower percentage stays inside a higher one. Older
// releases keep whatever value they were built with, so during a ramp the fleet
// runs several percentages at once.
const cloudFunnelRolloutPercent = 20

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

// Memoized: the bucket verdict cannot change within a process (the key is
// sticky and the percent is compiled in), and capabilities are requested per
// page load — no reason to re-read the settings file each time. The env
// override above stays live deliberately.
var cloudFunnelBucketVerdict = sync.OnceValue(func() bool {
	return cloudFunnelBucketed(cloudFunnelRolloutPercent, bucketKey())
})

// bucketKey is the value the rollout hashes on. In-cluster it prefers the Radar
// Deployment's creation time over ~/.radar: that directory sits on an emptyDir
// that dies with the pod, so bucketing on it re-rolls the verdict on every
// restart and rollout — and disagrees between replicas — making the funnel
// flicker in and out.
//
// It still falls back to ~/.radar when the Deployment is unreadable (chart too
// old to set the downward-API env vars, no cluster access, RBAC without
// deployments). A flickering verdict is recoverable; returning nothing is not —
// cloudFunnelBucketed pins an empty key out of every partial rollout, so those
// installs would never see the funnel at any percentage below 100.
func bucketKey() string {
	if k8s.IsInCluster() {
		if installed := k8s.InstalledAt(context.Background()); installed != 0 {
			return strconv.FormatInt(installed, 10)
		}
	}
	return settings.RolloutKey()
}

func cloudFunnelBucketed(percent uint32, key string) bool {
	if percent >= 100 {
		return true
	}
	// An empty key means nothing stable to hash — stay out of a partial rollout
	// rather than re-rolling a fresh bucket every start.
	if percent == 0 || key == "" {
		return false
	}
	h := fnv.New32a()
	h.Write([]byte(key))
	return h.Sum32()%100 < percent
}
