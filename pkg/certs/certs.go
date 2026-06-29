// Package certs unifies a cluster's certificates from the two sources Radar
// knows about — cert-manager.io Certificate CRs and raw kubernetes.io/tls
// secrets — into one health-rated, deduplicated list.
//
// A cluster that doesn't run cert-manager has only TLS secrets; a cluster that
// does has both, and the same physical cert appears in each (the Certificate
// owns a Secret). Aggregate is the single place that reconciles them, so fleet
// callers (Radar Hub) stay thin pivots and never re-implement the merge.
package certs

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// Source identifies which input a cert row came from.
type Source string

const (
	SourceCertManager Source = "cert-manager"
	SourceTLSSecret   Source = "tls-secret"
)

// Health mirrors the k8s-ui cert-expiry vocabulary: healthy ≥30d, degraded
// 7–29d, unhealthy <7d or expired, unknown when there's no expiry to judge.
type Health string

const (
	HealthHealthy   Health = "healthy"
	HealthDegraded  Health = "degraded"
	HealthUnhealthy Health = "unhealthy"
	HealthUnknown   Health = "unknown"
)

const (
	degradedWithinDays  = 30
	unhealthyWithinDays = 7
)

// Input is one certificate normalized from either source. The caller (the
// in-cluster handler) does the k8s I/O + projection; this package owns the
// reconciliation. NotAfter == nil means expiry is unknown (a cert-manager
// Certificate that hasn't been issued yet has no status.notAfter).
type Input struct {
	Name      string
	Namespace string
	Issuer    string
	Domains   []string
	NotAfter  *time.Time
	Source    Source
	// SecretName is set only for cert-manager Certificates: the Secret the
	// Certificate writes to. Used to dedup the TLS-secret leg against the
	// authoritative Certificate row.
	SecretName string
}

// Sources bundles a cluster's per-source inputs for one Aggregate call.
type Sources struct {
	CertManager []Input
	TLSSecrets  []Input
	// Now anchors expiry math; zero value falls back to time.Now (tests pin it).
	Now time.Time
}

// Cert is one unified row, ready to serialize. DaysLeft is nil when expiry is
// unknown; Expiry is RFC3339 and omitted in that case.
type Cert struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Issuer    string `json:"issuer"`
	Domains   string `json:"domains"`
	Expiry    string `json:"expiry,omitempty"`
	DaysLeft  *int   `json:"daysLeft,omitempty"`
	Health    Health `json:"health"`
	Source    Source `json:"source"`
}

// Aggregate merges the two sources into a single most-urgent-first list. A TLS
// secret owned by a cert-manager Certificate (matched on namespace + secretName)
// is dropped in favor of the Certificate row, so the same physical cert is
// counted once. Inputs are otherwise passed through verbatim.
func Aggregate(s Sources) []Cert {
	now := s.Now
	if now.IsZero() {
		now = time.Now()
	}

	out := make([]Cert, 0, len(s.CertManager)+len(s.TLSSecrets))
	managed := make(map[string]bool, len(s.CertManager))
	for _, in := range s.CertManager {
		out = append(out, toCert(in, now))
		if in.SecretName != "" {
			managed[in.Namespace+"/"+in.SecretName] = true
		}
	}
	for _, in := range s.TLSSecrets {
		if managed[in.Namespace+"/"+in.Name] {
			continue
		}
		out = append(out, toCert(in, now))
	}

	sort.SliceStable(out, func(i, j int) bool { return less(out[i], out[j]) })
	return out
}

func toCert(in Input, now time.Time) Cert {
	c := Cert{
		Name:      in.Name,
		Namespace: in.Namespace,
		Issuer:    in.Issuer,
		Domains:   formatDomains(in.Domains),
		Health:    HealthUnknown,
		Source:    in.Source,
	}
	if in.NotAfter != nil {
		c.Expiry = in.NotAfter.UTC().Format(time.RFC3339)
		// Floor (not truncate-toward-zero) so a cert that expired an hour ago
		// reads as -1 ("expired"), not 0 ("expires today"). Matches the floor
		// semantics in topology.ParsePEMCertificates.
		days := int(math.Floor(in.NotAfter.Sub(now).Hours() / 24))
		c.DaysLeft = &days
		switch {
		case days < unhealthyWithinDays:
			c.Health = HealthUnhealthy
		case days < degradedWithinDays:
			c.Health = HealthDegraded
		default:
			c.Health = HealthHealthy
		}
	}
	return c
}

// less sorts most-urgent-first: known expiry ascending by days left, unknown
// expiry sinks to the bottom, ties broken by namespace then name so refetches
// present a stable order.
func less(a, b Cert) bool {
	switch {
	case a.DaysLeft == nil && b.DaysLeft == nil:
		// both unknown — fall through to lexical tiebreak
	case a.DaysLeft == nil:
		return false
	case b.DaysLeft == nil:
		return true
	case *a.DaysLeft != *b.DaysLeft:
		return *a.DaysLeft < *b.DaysLeft
	}
	if a.Namespace != b.Namespace {
		return a.Namespace < b.Namespace
	}
	return a.Name < b.Name
}

// formatDomains abbreviates SANs to "a, b +N more" beyond two so a
// wildcard-plus-many-hosts cert stays one table-cell wide.
func formatDomains(d []string) string {
	switch len(d) {
	case 0:
		return ""
	case 1:
		return d[0]
	case 2:
		return d[0] + ", " + d[1]
	default:
		return fmt.Sprintf("%s, %s +%d more", d[0], d[1], len(d)-2)
	}
}
