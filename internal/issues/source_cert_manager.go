package issues

import (
	"fmt"
	"strings"
	"time"

	"github.com/skyhook-io/radar/pkg/timeutil"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const ReasonCertificateRenewalFailed = "CertificateRenewalFailed"

func detectCertificateRenewalFailure(gvr schema.GroupVersionResource, u *unstructured.Unstructured, now time.Time) []Issue {
	if u.GetDeletionTimestamp() != nil || u.GetGeneration() <= 0 {
		return nil
	}
	policy, _, _ := unstructured.NestedString(u.Object, "spec", "renewal", "policy")
	if policy == "Disabled" {
		return nil
	}
	attempts, _, _ := unstructured.NestedInt64(u.Object, "status", "failedIssuanceAttempts")
	if attempts <= 0 {
		return nil
	}
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	ready, issuing, issuingMessage := "", "", ""
	for _, raw := range conds {
		condition, ok := raw.(map[string]any)
		if !ok || condition["observedGeneration"] != u.GetGeneration() {
			continue
		}
		status, _ := condition["status"].(string)
		switch condition["type"] {
		case "Ready":
			ready = status
		case "Issuing":
			issuing = status
			issuingMessage, _ = condition["message"].(string)
		}
	}
	if ready != "True" || (issuing != "False" && issuing != "True") {
		return nil
	}
	times := make(map[string]time.Time, 4)
	for _, field := range []string{"notBefore", "notAfter", "renewalTime", "lastFailureTime"} {
		value, _, _ := unstructured.NestedString(u.Object, "status", field)
		at, err := time.Parse(time.RFC3339, value)
		if err != nil || at.IsZero() {
			return nil
		}
		times[field] = at
	}
	start, expiry, due, failed := times["notBefore"], times["notAfter"], times["renewalTime"], times["lastFailureTime"]
	// A spec-triggered reissue before renewal is due is not a failed renewal.
	if due.Before(start) || !due.Before(expiry) || failed.Before(due) || failed.After(now) {
		return nil
	}
	attemptLabel := "attempt"
	if attempts != 1 {
		attemptLabel = "attempts"
	}
	severity := SeverityWarning
	validity := fmt.Sprintf("The recorded certificate expires in %s (%s); cert-manager reports Ready.", timeutil.FormatAgeShort(expiry.Sub(now)), expiry.UTC().Format(time.RFC3339))
	if !now.Before(expiry) {
		severity = SeverityCritical
		validity = fmt.Sprintf("The recorded certificate expired at %s despite cert-manager still reporting Ready. Check the current certificate and serving endpoint.", expiry.UTC().Format(time.RFC3339))
	}
	message := fmt.Sprintf("Certificate renewal failed (%d consecutive failed issuance %s; last failure %s ago, %s). %s", attempts, attemptLabel, timeutil.FormatAgeShort(now.Sub(failed)), failed.UTC().Format(time.RFC3339), validity)
	if issuing == "True" {
		message += " A retry is in progress; renewal has not yet succeeded."
	} else if detail := strings.TrimSpace(issuingMessage); detail != "" {
		runes := []rune(detail)
		if len(runes) > 500 {
			detail = string(runes[:500]) + "…"
		}
		message += " Controller report: " + detail
	}
	// The latest failure moves on retries; it is not the start of this episode.
	issue := newConditionIssue(gvr, "Certificate", u.GetNamespace(), u.GetName(), severity,
		ReasonCertificateRenewalFailed, message, time.Time{}, false, "", u.GetCreationTimestamp().Time)
	issue.Action = "Inspect this Certificate's conditions and events, its current CertificateRequest, and its issuer. Resolve the reported issuance error and confirm a successful renewal."
	return []Issue{issue}
}
