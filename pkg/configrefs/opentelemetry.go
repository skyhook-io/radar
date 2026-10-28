package configrefs

import "strings"

// EffectiveInstrumentationAnnotation follows the OTel operator's Pod/Namespace
// precedence. In particular Pod=true inherits a named Namespace selection;
// Pod=false or a named Pod selection wins.
func EffectiveInstrumentationAnnotation(namespace, pod map[string]string, key string) (value string, fromNamespace bool) {
	p, n := pod[key], namespace[key]
	switch {
	case n == "":
		return p, false
	case p == "":
		return n, true
	case !strings.EqualFold(p, "true"):
		return p, false
	case strings.EqualFold(n, "false"):
		return p, false
	default:
		return n, true
	}
}
