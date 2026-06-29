package cloud

import (
	"log"
	"os"
	"strconv"
	"sync/atomic"
)

// cloudModeEnvVar is the boolean env var the Helm chart sets when
// `cloud.enabled: true`. We accept any value `strconv.ParseBool`
// recognizes (true / 1 / t / T / TRUE / True, and the false analogues)
// rather than the strict `== "true"` an earlier implementation used —
// a typo'd `True` or `1` silently degrading a Cloud deployment to OSS
// mode is a settings-leak liability (theme/pinnedKinds get persisted
// to the shared in-cluster Radar's settings.json instead of Cloud's
// per-user store).
const cloudModeEnvVar = "RADAR_CLOUD_MODE"

// warnedUnparseable rate-limits the unparseable-value warning to one
// emission per process so a malformed env var doesn't spam logs on
// every per-request capability check.
var warnedUnparseable atomic.Bool

// Mode reports whether Radar is running under Radar Cloud. The env
// var is re-read on each call (cheap — single syscall) so tests using
// t.Setenv work as expected. The unparseable-value warning is rate-
// limited via warnedUnparseable; a misconfigured env logs once at the
// first read.
func Mode() bool {
	raw, present := os.LookupEnv(cloudModeEnvVar)
	if !present || raw == "" {
		return false
	}
	parsed, err := strconv.ParseBool(raw)
	if err != nil {
		// A set-but-unparseable env var is operator misconfiguration.
		// Default to false (OSS mode) but loudly so it surfaces in
		// chart-install logs rather than silently degrading multi-
		// tenant settings to a per-user store. CompareAndSwap ensures
		// we log only once per process even though Mode() is called
		// per request.
		if warnedUnparseable.CompareAndSwap(false, true) {
			log.Printf("[cloud] WARNING: %s=%q is not a valid bool — treating as false. Use 'true' or 'false'.", cloudModeEnvVar, raw)
		}
		return false
	}
	return parsed
}

// LogStartupMode logs the resolved cloud-mode value once at process
// start. Call from main after flag parsing. Logging both true and false
// makes the deployment topology obvious in startup logs and aids
// support-debugging when a Cloud install accidentally boots in OSS
// mode.
func LogStartupMode() {
	log.Printf("[cloud] cloud mode: %t (%s=%q)", Mode(), cloudModeEnvVar, os.Getenv(cloudModeEnvVar))
}
