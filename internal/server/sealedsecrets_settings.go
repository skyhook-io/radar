package server

import (
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

// sealedSecretsEnvPrefix is how the controller exposes every flag as an
// environment variable: SEALED_SECRETS_<FLAG>, dashes as underscores.
const sealedSecretsEnvPrefix = "SEALED_SECRETS_"

// Key-order values the controller accepts for --key-order-priority.
const (
	keyOrderCertNotBefore = "certNotBefore"
	keyOrderCreationTime  = "creationTime"
	keyOrderUnknown       = "unknown"
)

// controllerSetting is one controller flag as Radar could read it from the
// Deployment. Unknown is true when the value comes from somewhere Radar can't
// see (a ConfigMap or Secret reference, envFrom, or a $(VAR) in the args).
type controllerSetting struct {
	Value   string
	Set     bool
	Unknown bool
}

// sealedSecretsSettings are the controller flags that decide which keys it
// seals with and which namespaces it serves.
type sealedSecretsSettings struct {
	KeyOrder             controllerSetting
	KeyPrefix            controllerSetting
	AllNamespaces        controllerSetting
	AdditionalNamespaces controllerSetting
	LabelSelector        controllerSetting
}

var sealedSecretsFlags = []string{"key-order-priority", "key-prefix", "all-namespaces", "additional-namespaces", "label-selector"}

var sealedSecretsBoolFlags = map[string]bool{"all-namespaces": true}

// readSealedSecretsSettings reads the controller's flags from its Deployment
// the way the controller resolves them: an argument wins over the matching
// SEALED_SECRETS_* environment variable, which wins over the default.
func readSealedSecretsSettings(d *appsv1.Deployment) sealedSecretsSettings {
	c := sealedSecretsContainer(d)
	values := map[string]controllerSetting{}
	if c != nil {
		fromEnvFrom := len(c.EnvFrom) > 0
		for _, name := range sealedSecretsFlags {
			env := sealedSecretsEnvPrefix + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
			for _, e := range c.Env {
				if e.Name != env {
					continue
				}
				if e.ValueFrom != nil {
					values[name] = controllerSetting{Set: true, Unknown: true}
				} else {
					values[name] = controllerSetting{Value: e.Value, Set: true}
				}
			}
			if fromEnvFrom && !values[name].Set {
				// An envFrom source may define it; Radar can't see which.
				values[name] = controllerSetting{Unknown: true}
			}
		}
		args := c.Args
		if len(c.Command) > 1 {
			args = append(append([]string{}, c.Command[1:]...), c.Args...)
		}
		for name, v := range parseSealedSecretsArgs(args) {
			values[name] = v
		}
	}
	return sealedSecretsSettings{
		KeyOrder:             values["key-order-priority"],
		KeyPrefix:            values["key-prefix"],
		AllNamespaces:        values["all-namespaces"],
		AdditionalNamespaces: values["additional-namespaces"],
		LabelSelector:        values["label-selector"],
	}
}

// parseSealedSecretsArgs reads --flag=value and --flag value (pflag syntax;
// a bool flag takes no separate value). The last occurrence wins.
func parseSealedSecretsArgs(args []string) map[string]controllerSetting {
	wanted := map[string]bool{}
	for _, f := range sealedSecretsFlags {
		wanted[f] = true
	}
	out := map[string]controllerSetting{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") {
			continue
		}
		name, value, hasValue := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		if !wanted[name] {
			continue
		}
		if !hasValue {
			if sealedSecretsBoolFlags[name] {
				value = "true"
			} else if i+1 < len(args) {
				i++
				value = args[i]
			}
		}
		out[name] = controllerSetting{Value: value, Set: true, Unknown: strings.Contains(value, "$(")}
	}
	return out
}

// sealedSecretsContainer is the controller container: the one that mentions
// a controller flag or variable or runs a sealed-secrets image, else the only
// container.
func sealedSecretsContainer(d *appsv1.Deployment) *corev1.Container {
	if d == nil {
		return nil
	}
	cs := d.Spec.Template.Spec.Containers
	for i := range cs {
		if strings.Contains(cs[i].Image, "sealed-secrets") || mentionsSealedSecretsFlag(&cs[i]) {
			return &cs[i]
		}
	}
	if len(cs) == 1 {
		return &cs[0]
	}
	return nil
}

func mentionsSealedSecretsFlag(c *corev1.Container) bool {
	for _, e := range c.Env {
		if strings.HasPrefix(e.Name, sealedSecretsEnvPrefix) {
			return true
		}
	}
	for _, a := range append(append([]string{}, c.Command...), c.Args...) {
		for _, f := range sealedSecretsFlags {
			if a == "--"+f || strings.HasPrefix(a, "--"+f+"=") {
				return true
			}
		}
	}
	return false
}

// keyOrder is how the controller orders its keys to pick the one it seals
// with: certificate NotBefore by default, or Secret creation time.
func (s sealedSecretsSettings) keyOrder() string {
	switch {
	case s.KeyOrder.Unknown:
		return keyOrderUnknown
	case !s.KeyOrder.Set, s.KeyOrder.Value == "CertNotBefore":
		return keyOrderCertNotBefore
	case s.KeyOrder.Value == "SecretCreationTimestamp":
		return keyOrderCreationTime
	}
	// The controller logs an invalid value and falls back to NotBefore.
	return keyOrderCertNotBefore
}

// legacyKeyName is the Secret the controller also loads by name, whatever
// its label: the one named by --key-prefix. Empty when unknown.
func (s sealedSecretsSettings) legacyKeyName() string {
	switch {
	case s.KeyPrefix.Unknown:
		return ""
	case !s.KeyPrefix.Set || s.KeyPrefix.Value == "":
		return legacySealingKeyName
	}
	return s.KeyPrefix.Value
}

// Coverage of one controller for one namespace.
type controllerCoverage int

const (
	coverageNone     controllerCoverage = iota
	coverageAll                         // watches every namespace
	coverageExplicit                    // its own namespace or one it lists
	coverageUnknown                     // Radar can't read whether it does
)

// coverageOf says whether controller d serves SealedSecrets in namespace,
// as the controller decides it: with --additional-namespaces it watches its
// own namespace plus that list (even when --all-namespaces is on); with
// --all-namespaces=false only its own; otherwise every namespace. A
// --label-selector narrows it to labelled SealedSecrets, which this read
// can't check, so a controller that has one reads as unknown.
func coverageOf(d *appsv1.Deployment, s sealedSecretsSettings, namespace string) controllerCoverage {
	if s.AllNamespaces.Unknown || s.AdditionalNamespaces.Unknown || s.LabelSelector.Unknown {
		return coverageUnknown
	}
	var cov controllerCoverage
	switch {
	case s.AdditionalNamespaces.Value != "":
		cov = coverageNone
		if namespace == d.Namespace {
			cov = coverageExplicit
		}
		for _, ns := range strings.Split(s.AdditionalNamespaces.Value, ",") {
			if ns == namespace {
				cov = coverageExplicit
			}
		}
	case s.AllNamespaces.Set && s.AllNamespaces.Value == "false":
		cov = coverageNone
		if namespace == d.Namespace {
			cov = coverageExplicit
		}
	case s.AllNamespaces.Set && s.AllNamespaces.Value != "true":
		return coverageUnknown
	default:
		cov = coverageAll
	}
	if cov != coverageNone && s.LabelSelector.Value != "" {
		return coverageUnknown
	}
	return cov
}

// Why a choice is ambiguous.
const (
	ambiguousOverlap      = "overlap"
	ambiguousUnknownScope = "unknownScope"
)

// chooseControllerFor picks the controller that serves SealedSecrets in
// namespace. A controller that names the namespace (its own, or listed)
// beats one that watches every namespace, since it is that namespace's
// dedicated controller. Among controllers that watch every namespace,
// kubeseal's default kube-system/sealed-secrets-controller wins, then the
// only one in kube-system, so a look-alike Deployment elsewhere can't blank
// the answer. Otherwise several candidates are ambiguous, never resolved by
// sort order. The only candidate on the cluster is chosen even when its
// scope can't be read.
func chooseControllerFor(candidates []*appsv1.Deployment, namespace string) (chosen *appsv1.Deployment, settings sealedSecretsSettings, ambiguousReason string, watched bool) {
	if len(candidates) == 1 {
		s := readSealedSecretsSettings(candidates[0])
		if coverageOf(candidates[0], s, namespace) == coverageNone {
			return nil, sealedSecretsSettings{}, "", false
		}
		return candidates[0], s, "", true
	}
	type cand struct {
		d *appsv1.Deployment
		s sealedSecretsSettings
	}
	var explicit, all, unknown []cand
	for _, d := range candidates {
		s := readSealedSecretsSettings(d)
		switch coverageOf(d, s, namespace) {
		case coverageExplicit:
			explicit = append(explicit, cand{d, s})
		case coverageAll:
			all = append(all, cand{d, s})
		case coverageUnknown:
			unknown = append(unknown, cand{d, s})
		}
	}
	switch {
	case len(explicit) > 1:
		return nil, sealedSecretsSettings{}, ambiguousOverlap, true
	case len(unknown) > 0:
		return nil, sealedSecretsSettings{}, ambiguousUnknownScope, true
	case len(explicit) == 1:
		return explicit[0].d, explicit[0].s, "", true
	case len(all) == 0:
		return nil, sealedSecretsSettings{}, "", false
	case len(all) == 1:
		return all[0].d, all[0].s, "", true
	}
	ds := make([]*appsv1.Deployment, len(all))
	for i, c := range all {
		ds[i] = c.d
	}
	if d, ambiguous := findSealedSecretsController(ds); d != nil && !ambiguous {
		for _, c := range all {
			if c.d == d {
				return c.d, c.s, "", true
			}
		}
	}
	return nil, sealedSecretsSettings{}, ambiguousOverlap, true
}

// isSealedSecretsControllerCandidate: labelled app.kubernetes.io/name=
// sealed-secrets or named sealed-secrets-controller.
func isSealedSecretsControllerCandidate(d *appsv1.Deployment) bool {
	return d != nil && (d.Labels["app.kubernetes.io/name"] == "sealed-secrets" || d.Name == sealedSecretsControllerName)
}
