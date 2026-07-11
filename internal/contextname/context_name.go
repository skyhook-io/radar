package contextname

import "regexp"

// Keep these provider patterns aligned with packages/k8s-ui/src/utils/context-name.ts.
var (
	gkeContextRe    = regexp.MustCompile(`^gke_([a-z][a-z0-9-]+)_([a-z][a-z0-9-]*[0-9][a-z0-9-]*)_(.+)$`)
	eksArnContextRe = regexp.MustCompile(`^arn:aws:eks:([^:]+):(\d+):cluster/(.+)$`)
	eksctlContextRe = regexp.MustCompile(`^(.+)@([^.]+)\.([^.]+)\.eksctl\.io$`)
	aksContextRe    = regexp.MustCompile(`^cluster(?:User|Admin)_([^_]+)_(.+)$`)
)

// ShortName extracts the human-facing cluster segment from provider-generated
// kubeconfig context names and preserves user-defined names unchanged.
func ShortName(contextName string) string {
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
