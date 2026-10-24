package cloud

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

var clusterUIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// IsClusterUID reports whether s has the shape of a Kubernetes UID.
func IsClusterUID(s string) bool {
	return clusterUIDPattern.MatchString(s)
}

// DiscoverClusterUID returns the kube-system namespace UID, the stable
// identity of this cluster for as long as the cluster exists.
func DiscoverClusterUID(ctx context.Context, client kubernetes.Interface) (string, error) {
	if client == nil {
		return "", errors.New("no Kubernetes client")
	}
	ns, err := client.CoreV1().Namespaces().Get(ctx, metav1.NamespaceSystem, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("read namespace kube-system: %w", err)
	}
	uid := string(ns.UID)
	if !IsClusterUID(uid) {
		return "", fmt.Errorf("namespace kube-system has an unexpected UID %q", uid)
	}
	return uid, nil
}

// Name sources sent with an enroll request.
const (
	NameSourceValues    = "values"
	NameSourceAPIServer = "api_server"
	NameSourceUID       = "uid"
)

// MaxClusterNameRunes is the Hub's limit on a cluster's display name.
const MaxClusterNameRunes = 80

// NameOptions are the operator's naming choices for an enrolling cluster.
type NameOptions struct {
	// Exact is used as the name unchanged, and Prefix/Suffix are ignored.
	Exact  string
	Prefix string
	Suffix string
}

// Validate rejects naming values that can never produce a valid name.
func (o NameOptions) Validate() error {
	if o.Exact != "" {
		if n := utf8.RuneCountInString(strings.TrimSpace(o.Exact)); n == 0 || n > MaxClusterNameRunes {
			return fmt.Errorf("--cluster-name must be 1 to %d characters", MaxClusterNameRunes)
		}
		return nil
	}
	// Leave room for at least the 16-character "cluster-<uid>" fallback.
	if utf8.RuneCountInString(o.Prefix)+utf8.RuneCountInString(o.Suffix) > MaxClusterNameRunes-16 {
		return fmt.Errorf("--cluster-name-prefix and --cluster-name-suffix together must be at most %d characters", MaxClusterNameRunes-16)
	}
	return nil
}

// ResolveClusterName picks the name an enrolling cluster proposes, and where it
// came from: the exact name from values, else prefix + the API server host +
// suffix, else prefix + "cluster-" + the first 8 characters of the cluster
// UID + suffix. A derived name longer than the Hub allows is shortened in its
// middle part, so the operator's prefix and suffix survive.
func ResolveClusterName(opts NameOptions, apiServerURL, clusterUID string) (name, source string) {
	if exact := strings.TrimSpace(opts.Exact); exact != "" {
		return exact, NameSourceValues
	}
	base, source := apiServerHost(apiServerURL), NameSourceAPIServer
	if base == "" {
		short := clusterUID
		if len(short) > 8 {
			short = short[:8]
		}
		base, source = "cluster-"+short, NameSourceUID
	}
	room := MaxClusterNameRunes - utf8.RuneCountInString(opts.Prefix) - utf8.RuneCountInString(opts.Suffix)
	if room < 1 {
		room = 1
	}
	if utf8.RuneCountInString(base) > room {
		base = string([]rune(base)[:room])
	}
	return opts.Prefix + base + opts.Suffix, source
}

func apiServerHost(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}
