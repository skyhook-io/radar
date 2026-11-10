package k8s

import (
	"slices"
	"strings"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const maxOrderComparedValueBytes = 64 * 1024

// sameEntriesReordered reports whether newValue holds exactly the
// comma-separated entries of oldValue, each as often, in a different order.
// Commas inside brackets don't split, so a Ceph mon_host entry such as
// "[v2:10.0.0.1:3300,v1:10.0.0.1:6789]" stays one entry. Anything it can't
// split with confidence (quotes, escapes, control characters, unbalanced
// brackets, empty entries, a single entry) reports false, which callers treat
// as a real change.
func sameEntriesReordered(oldValue, newValue string) bool {
	if oldValue == newValue {
		return false
	}
	oldEntries, ok := topLevelCommaEntries(oldValue)
	if !ok {
		return false
	}
	newEntries, ok := topLevelCommaEntries(newValue)
	if !ok || len(oldEntries) != len(newEntries) {
		return false
	}
	slices.Sort(oldEntries)
	slices.Sort(newEntries)
	return slices.Equal(oldEntries, newEntries)
}

func topLevelCommaEntries(value string) ([]string, bool) {
	if len(value) > maxOrderComparedValueBytes || !utf8.ValidString(value) {
		return nil, false
	}
	var entries []string
	var open []rune
	start := 0
	for i, r := range value {
		switch {
		case r < 0x20 || r == 0x7f || r == '"' || r == '\'' || r == '`' || r == '\\':
			return nil, false
		case r == '[' || r == '(' || r == '{':
			open = append(open, r)
		case r == ']' || r == ')' || r == '}':
			if len(open) == 0 || open[len(open)-1] != matchingOpenBracket(r) {
				return nil, false
			}
			open = open[:len(open)-1]
		case r == ',' && len(open) == 0:
			entries = append(entries, value[start:i])
			start = i + 1
		}
	}
	if len(open) != 0 {
		return nil, false
	}
	entries = append(entries, value[start:])
	if len(entries) < 2 || slices.Contains(entries, "") {
		return nil, false
	}
	return entries, true
}

func matchingOpenBracket(r rune) rune {
	switch r {
	case ']':
		return '['
	case ')':
		return '('
	default:
		return '{'
	}
}

// reorderedValueKeys returns the keys of modified whose values are
// sameEntriesReordered. Only string and []byte values are compared.
func reorderedValueKeys[T any](oldData, newData map[string]T, modified []string) []string {
	var out []string
	for _, key := range modified {
		oldText, okOld := orderComparableText(oldData[key])
		newText, okNew := orderComparableText(newData[key])
		if okOld && okNew && sameEntriesReordered(oldText, newText) {
			out = append(out, key)
		}
	}
	return out
}

func orderComparableText(value any) (string, bool) {
	switch v := value.(type) {
	case string:
		return v, true
	case []byte:
		return string(v), true
	default:
		return "", false
	}
}

// Rook writes the Ceph monitor list to this Secret on every reconcile by
// ranging over a Go map, so the joined order changes without any monitor
// changing (rook pkg/operator/ceph/config/store.go, StoreName, and
// pkg/daemon/ceph/client/config.go, PopulateMonHostMembers). Ceph treats
// both values as sets of monitors. Mon pods read them as env vars, so a
// reorder alone must not ask for a mon or OSD restart.
const (
	rookMonConfigSecretName = "rook-ceph-config"
	rookSecretType          = corev1.SecretType("kubernetes.io/rook")
)

var rookCephClusterGroupKind = schema.GroupKind{Group: "ceph.rook.io", Kind: "CephCluster"}

// isRookMonOrderInsensitiveKey reports whether key is one of the monitor list
// keys of Rook's own mon config Secret: the name and type Rook gives it and a
// CephCluster as its controller. A lookalike without that owner never matches.
func isRookMonOrderInsensitiveKey(secret *corev1.Secret, key string) bool {
	if secret == nil || secret.Name != rookMonConfigSecretName || secret.Type != rookSecretType {
		return false
	}
	if key != "mon_host" && key != "mon_initial_members" {
		return false
	}
	owner := metav1.GetControllerOf(secret)
	if owner == nil {
		return false
	}
	gv, err := schema.ParseGroupVersion(owner.APIVersion)
	return err == nil && (schema.GroupKind{Group: gv.Group, Kind: owner.Kind}) == rookCephClusterGroupKind
}

// ReorderedKeysPathSuffix marks a Secret or ConfigMap diff field listing the
// modified keys whose new value only reorders the old one's comma-separated
// entries ("data (reordered keys)"). Those keys are also listed under
// "(modified keys)".
const ReorderedKeysPathSuffix = " (reordered keys)"

// reorderedKeysByField returns, per data field ("data", "stringData"), the
// keys one diff listed under "<field> (reordered keys)".
func reorderedKeysByField(fields []FieldChange) map[string]map[string]bool {
	var out map[string]map[string]bool
	for _, field := range fields {
		base, ok := strings.CutSuffix(field.Path, ReorderedKeysPathSuffix)
		if !ok {
			continue
		}
		if out == nil {
			out = make(map[string]map[string]bool)
		}
		if out[base] == nil {
			out[base] = make(map[string]bool)
		}
		for _, key := range stringValues(field.NewValue) {
			out[base][key] = true
		}
	}
	return out
}

// keyListFieldBase returns "data" for "data (modified keys)".
func keyListFieldBase(path string) string {
	base, _, _ := strings.Cut(path, " (")
	return base
}
