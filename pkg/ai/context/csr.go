package context

import (
	"encoding/json"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/csr"
)

// summarizeCertificateSigningRequest says where a request stands between
// approval and issuance. Read generically, an approved request whose signer
// failed would report its first condition, Approved.
func summarizeCertificateSigningRequest(obj *unstructured.Unstructured) *ResourceSummary {
	status := csr.StatusOf(obj.Object)
	s := &ResourceSummary{
		Kind:   csr.Kind,
		Name:   obj.GetName(),
		Status: status.State.Label(),
		Age:    age(obj.GetCreationTimestamp().Time),
	}
	s.Type, _, _ = unstructured.NestedString(obj.Object, "spec", "signerName")
	switch {
	case status.State == csr.StateDenied || status.State == csr.StateFailed:
		s.Issue = conditionText(status.Condition)
	case status.CertificatePresent && status.State != csr.StateIssued:
		s.Issue = "certificate returned without an Approved condition"
	}
	return s
}

func conditionText(c *csr.Condition) string {
	if c == nil {
		return ""
	}
	if c.Reason != "" && c.Message != "" {
		return c.Reason + ": " + c.Message
	}
	if c.Reason != "" {
		return c.Reason
	}
	return c.Message
}

// decodeCertificateSigningRequest replaces a request's base64 PEM payloads
// with what they say: an agent cannot read DER, and the blobs cost thousands
// of tokens.
func decodeCertificateSigningRequest(m map[string]any) {
	if !csr.IsObject(m) {
		return
	}
	details := csr.Describe(m, time.Now())
	if spec, ok := m["spec"].(map[string]any); ok && spec["request"] != nil {
		spec["request"] = jsonValue(details.Request)
	}
	if status, ok := m["status"].(map[string]any); ok && status["certificate"] != nil {
		status["certificate"] = jsonValue(details.Certificate)
	}
}

// jsonValue keeps the object made of plain JSON values, which the redaction
// walk and deep copies of minified objects expect.
func jsonValue(v any) any {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}
