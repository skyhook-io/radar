package main

import "regexp"

// Patterns mirror packages/k8s-ui/src/utils/context-name.ts. Keep in sync —
// drift here means the OS window title and the in-page cluster selector
// will render the same cluster differently, which is the whole class of
// bug we're avoiding.
//
// RE2 differences from the TS version:
//   - No lookahead, so the GKE region segment requires a digit via a
//     sequential `[a-z0-9-]*[0-9][a-z0-9-]*` instead of TS's
//     `(?=[a-z0-9-]*\d)([a-z][a-z0-9-]*)`. Same language, no backtracking
//     concerns (RE2 is linear-time by construction).
var (
	gkeContextRe    = regexp.MustCompile(`^gke_([a-z][a-z0-9-]+)_([a-z][a-z0-9-]*[0-9][a-z0-9-]*)_(.+)$`)
	eksArnContextRe = regexp.MustCompile(`^arn:aws:eks:([^:]+):(\d+):cluster/(.+)$`)
	eksctlContextRe = regexp.MustCompile(`^(.+)@([^.]+)\.([^.]+)\.eksctl\.io$`)
	aksContextRe    = regexp.MustCompile(`^cluster(?:User|Admin)_([^_]+)_(.+)$`)
)

// clusterShortName extracts the human-friendly cluster name from a kubeconfig
// context name (GKE / EKS ARN / eksctl / AKS shapes). Falls back to the raw
// context name when the shape isn't recognized, so user-named contexts and
// the in-cluster sentinel pass through untouched.
func clusterShortName(contextName string) string {
	if m := gkeContextRe.FindStringSubmatch(contextName); m != nil {
		return m[3]
	}
	if m := eksArnContextRe.FindStringSubmatch(contextName); m != nil {
		return m[3]
	}
	if m := eksctlContextRe.FindStringSubmatch(contextName); m != nil {
		return m[2]
	}
	if m := aksContextRe.FindStringSubmatch(contextName); m != nil {
		return m[2]
	}
	return contextName
}
