package contextname

import (
	"regexp"
	"strconv"
	"strings"
)

// Keep these provider patterns aligned with packages/k8s-ui/src/utils/context-name.ts.
var (
	gkeContextRe    = regexp.MustCompile(`^gke_([a-z][a-z0-9-]+)_([a-z][a-z0-9-]*[0-9][a-z0-9-]*)_(.+)$`)
	eksArnContextRe = regexp.MustCompile(`^arn:aws:eks:([^:]+):(\d+):cluster/(.+)$`)
	eksctlContextRe = regexp.MustCompile(`^(.+)@([^.]+)\.([^.]+)\.eksctl\.io$`)
	aksContextRe    = regexp.MustCompile(`^cluster(?:User|Admin)_([^_]+)_(.+)$`)
	// oc login: <project>/<server host, dots as dashes, or [IPv6]>:<port>/<user>
	openshiftContextRe = regexp.MustCompile(`^([a-z0-9][a-z0-9-]*)/([a-z0-9][a-z0-9-]*|\[[0-9a-fA-F:.]+\]):(\d{1,5})/(.+)$`)
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
	if m := openshiftContextRe.FindStringSubmatch(contextName); m != nil {
		port, _ := strconv.Atoi(m[3])
		if !strings.HasSuffix(m[1], "-") && !strings.HasSuffix(m[2], "-") && port >= 1 && port <= 65535 {
			return m[2]
		}
	}
	return contextName
}
