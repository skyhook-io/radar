package datum

import (
	"github.com/skyhook-io/radar/pkg/conditions"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"strings"
)

type Condition struct {
	Scope, Path, Type, Status, Reason, Message, Transition string
	Stale, Transient                                       bool
}

func Observations(u *unstructured.Unstructured) []Condition {
	var out []Condition
	appendConditions := func(obj map[string]any, scope, path string) {
		items, _, _ := unstructured.NestedSlice(obj, "conditions")
		for _, raw := range items {
			m, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			s := func(k string) string { v, _ := m[k].(string); return v }
			generation, has, _ := unstructured.NestedInt64(m, "observedGeneration")
			if !has && u.GetKind() == "Workload" {
				generation, has, _ = unstructured.NestedInt64(u.Object, "status", "observedGeneration")
			}
			c := Condition{Scope: scope, Path: path + ".conditions[" + s("type") + "]", Type: s("type"), Status: s("status"), Reason: s("reason"), Message: s("message"), Transition: s("lastTransitionTime"), Stale: has && generation < u.GetGeneration()}
			c.Transient = InProgress(c.Reason)
			out = append(out, c)
		}
	}
	status, _, _ := unstructured.NestedMap(u.Object, "status")
	appendConditions(status, "", "status")
	for _, field := range []string{"hostnameStatuses", "recordSets", "capabilities"} {
		items, _, _ := unstructured.NestedSlice(status, field)
		for _, raw := range items {
			m, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			scope, _ := m["hostname"].(string)
			if scope == "" {
				scope, _ = m["name"].(string)
			}
			if scope == "" {
				scope, _ = m["type"].(string)
			}
			appendConditions(m, scope, "status."+field+"["+scope+"]")
		}
	}
	return out
}
func positive(kind, typ string) bool {
	if kind == "Domain" {
		return typ == "Verified" || typ == "ValidDomain"
	}
	switch typ {
	case "Accepted", "Programmed", "Ready", "Available", "CertificatesReady", "CertificateReady", "RecordProgrammed", "DNSRecordProgrammed", "DNSRecordsProgrammed", "HostnamesVerified", "Verified", "MembersResolved", "QuotaGranted":
		return true
	}
	return false
}
func Failures(u *unstructured.Unstructured) []Condition {
	var out []Condition
	if u.GetDeletionTimestamp() != nil {
		return out
	}
	observations := Observations(u)
	for _, c := range observations {
		if (positive(u.GetKind(), c.Type) && c.Status == "False" || u.GetKind() == "HTTPProxy" && c.Type == "HostnamesInUse" && c.Status == "True") && !c.Stale && !c.Transient {
			duplicate := false
			nestedType := map[string]string{"CertificatesReady": "CertificateReady", "DNSRecordsProgrammed": "DNSRecordProgrammed", "HostnamesVerified": "Verified", "HostnamesInUse": "Available", "Ready": "Ready"}[c.Type]
			if u.GetKind() == "DNSRecordSet" && c.Type == "Programmed" {
				nestedType = "RecordProgrammed"
			}
			if c.Scope == "" && nestedType != "" {
				for _, nested := range observations {
					if nested.Scope != "" && nested.Type == nestedType && nested.Status == "False" && !nested.Stale && !nested.Transient {
						duplicate = true
					}
				}
			}
			if !duplicate {
				out = append(out, c)
			}
		}
	}
	return out
}
func State(u *unstructured.Unstructured) string {
	if u.GetDeletionTimestamp() != nil {
		return "Terminating"
	}
	if len(Failures(u)) > 0 {
		return "Attention needed"
	}
	observed := false
	for _, c := range Observations(u) {
		if !positive(u.GetKind(), c.Type) {
			continue
		}
		observed = true
		if c.Stale || c.Transient {
			return "Reconciling"
		}
		if c.Status != "True" {
			return "Unknown"
		}
	}
	for _, typ := range RequiredConditions(u.GetKind()) {
		present := false
		for _, c := range Observations(u) {
			if c.Scope == "" && c.Type == typ && c.Status == "True" {
				present = true
				break
			}
		}
		if !present {
			return "Unknown"
		}
	}
	if observed {
		return "Ready"
	}
	return "Unknown"
}
func Hosts(u *unstructured.Unstructured) []string {
	hosts, _, _ := unstructured.NestedStringSlice(u.Object, "spec", "hostnames")
	domain, _, _ := unstructured.NestedString(u.Object, "spec", "domainName")
	if domain != "" {
		hosts = append(hosts, domain)
	}
	return hosts
}
func HostMatches(domain, hostname string) bool {
	domain = strings.ToLower(strings.TrimSuffix(domain, "."))
	hostname = strings.ToLower(strings.TrimSuffix(hostname, "."))
	return domain != "" && (hostname == domain || strings.HasSuffix(hostname, "."+domain))
}

func InProgress(reason string) bool {
	switch reason {
	case "PendingVerification", "ProgrammingInProgress", "WaitingForController", "Updating", "Provisioning", "Starting", "Stopping", "InstancesProvisioning", "PendingProgramming", "PendingQuota", "ChallengeInProgress", "CertificatesPending", "RetryPending", "PendingEvaluation":
		return true
	}
	return conditions.IsInProgressForIssues(reason)
}
func RequiredConditions(kind string) []string {
	switch kind {
	case "DNSZone", "DNSRecordSet", "HTTPProxy":
		return []string{"Accepted", "Programmed"}
	case "Domain":
		return []string{"ValidDomain", "Verified"}
	case "Connector":
		return []string{"Accepted", "Ready"}
	case "Workload", "Instance":
		return []string{"Available"}
	case "DNSZoneClass":
		return []string{"Accepted", "Programmed"}
	case "ConnectorAdvertisement":
		return []string{"Accepted"}
	default:
		return []string{"Ready"}
	}
}
