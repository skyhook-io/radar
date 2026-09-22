package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/k8s"
)

func TestDecodeBoundedJSONBody(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		limit   int64
		wantErr bool
	}{
		{name: "single object", body: `{"yaml":"ok"}`, limit: 64},
		{name: "trailing object", body: `{"yaml":"ok"}{"yaml":"again"}`, limit: 64, wantErr: true},
		{name: "oversized", body: `{"yaml":"too large"}`, limit: 8, wantErr: true},
		{name: "unknown field", body: `{"unexpected":true}`, limit: 64, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest("POST", "/", strings.NewReader(tt.body))
			var target yamlPreviewRequest
			err := decodeBoundedJSONBody(recorder, request, tt.limit, &target)
			if (err != nil) != tt.wantErr {
				t.Fatalf("decodeBoundedJSONBody() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestWriteApplyResourceErrorIncludesPartialResults(t *testing.T) {
	recorder := httptest.NewRecorder()
	server := &Server{}
	server.writeApplyResourceError(recorder, http.StatusUnprocessableEntity, "document 2: rejected", []k8s.ApplyResourceResult{{
		Kind: "Namespace",
		Name: "checkout",
	}}, 1, 3)

	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnprocessableEntity)
	}
	var response struct {
		Error       string                    `json:"error"`
		Results     []k8s.ApplyResourceResult `json:"results"`
		FailedIndex int                       `json:"failedIndex"`
		Total       int                       `json:"total"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error != "document 2: rejected" || len(response.Results) != 1 || response.FailedIndex != 1 || response.Total != 3 {
		t.Fatalf("unexpected response: %+v", response)
	}
}

func TestNormalizedPreviewYAML_StripsNoiseAndRedactsSecrets(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]any{
			"name":            "credentials",
			"resourceVersion": "42",
			"annotations":     map[string]any{"token": "annotation-secret"},
		},
		"data":   map[string]any{"password": "c2VjcmV0"},
		"status": map[string]any{"phase": "Ready"},
		"type":   "Opaque",
	}}

	live := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]any{
			"name":        "credentials",
			"annotations": map[string]any{"token": "annotation-secret"},
		},
		"data": map[string]any{
			"password": "b2xk",
			"username": "c2FtZQ==",
		},
	}}
	obj.Object["data"].(map[string]any)["username"] = "c2FtZQ=="
	content, baseline, redacted, err := normalizedPreviewYAML(obj, live, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
	}}, true)
	if err != nil {
		t.Fatalf("normalizedPreviewYAML failed: %v", err)
	}
	if !redacted {
		t.Fatal("Secret preview was not marked redacted")
	}
	for _, leaked := range []string{"c2VjcmV0", "b2xk", "c2FtZQ==", "annotation-secret", "resourceVersion", "status:"} {
		if strings.Contains(content, leaked) || strings.Contains(baseline, leaked) {
			t.Fatalf("preview leaked %q:\npredicted:\n%s\nbaseline:\n%s", leaked, content, baseline)
		}
	}
	if !strings.Contains(content, "type: Opaque") || !strings.Contains(content, "password:") {
		t.Fatalf("preview dropped useful Secret shape:\n%s", content)
	}
	if !strings.Contains(content, "<redacted:after>") || !strings.Contains(content, "<redacted:unchanged>") {
		t.Fatalf("predicted Secret does not distinguish changed and unchanged values:\n%s", content)
	}
	if !strings.Contains(baseline, "<redacted:before>") || !strings.Contains(baseline, "<redacted:unchanged>") {
		t.Fatalf("baseline Secret does not distinguish changed and unchanged values:\n%s", baseline)
	}
}

func TestNormalizedPreviewYAML_PreservesSubmittedStatusOnly(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "example.io/v1",
		"kind":       "Widget",
		"metadata":   map[string]any{"name": "demo"},
		"status":     map[string]any{"operatorNote": "requested"},
	}}
	live := obj.DeepCopy()
	submittedWithoutStatus := obj.DeepCopy()
	unstructured.RemoveNestedField(submittedWithoutStatus.Object, "status")

	withoutStatus, _, _, err := normalizedPreviewYAML(obj, live, submittedWithoutStatus, true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(withoutStatus, "status:") {
		t.Fatalf("server status noise was retained:\n%s", withoutStatus)
	}

	withStatus, baseline, _, err := normalizedPreviewYAML(obj, live, obj, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(withStatus, "status:") || !strings.Contains(baseline, "status:") {
		t.Fatalf("submitted status was hidden:\npredicted:\n%s\nbaseline:\n%s", withStatus, baseline)
	}
}

func TestPreviewErrorStatus(t *testing.T) {
	tests := []struct {
		name string
		err  error
		mode string
		want string
	}{
		{
			name: "dependency not found",
			err:  apierrors.NewNotFound(schema.GroupResource{Resource: "widgets"}, "later-doc"),
			mode: "apply",
			want: "unavailable",
		},
		{
			name: "update target deleted",
			err:  apierrors.NewNotFound(schema.GroupResource{Resource: "widgets"}, "edited-doc"),
			mode: "update",
			want: "rejected",
		},
		{name: "new CRD kind", err: errors.New("unknown resource kind: Widget"), want: "unavailable"},
		{name: "webhook dry run", err: errors.New("admission webhook does not support dry run"), want: "unavailable"},
		{name: "validation", err: errors.New("spec.replicas must be greater than zero"), want: "rejected"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := previewErrorStatus(tt.err, tt.mode); got != tt.want {
				t.Fatalf("previewErrorStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestUnknownLiveStateRequiresPreviewAcknowledgement(t *testing.T) {
	doc := yamlPreviewDocument{Status: "accepted", Action: "unknown"}
	requireAcknowledgementWithoutLiveState(&doc)
	if doc.Status != "unavailable" || !strings.Contains(doc.Error, "not be protected") {
		t.Fatalf("unknown live state did not require acknowledgement: %+v", doc)
	}

	verified := yamlPreviewDocument{Status: "accepted", Action: "update"}
	requireAcknowledgementWithoutLiveState(&verified)
	if verified.Status != "accepted" || verified.Error != "" {
		t.Fatalf("verified preview was changed: %+v", verified)
	}
}

func TestRejectedSecretPreviewDoesNotEchoSensitiveError(t *testing.T) {
	err := errors.New(`data.password: Invalid value: "c2VjcmV0": rejected`)
	doc := rejectedPreviewDocument(
		yamlPreviewDocument{Kind: "Secret", Name: "credentials"},
		"kind: Secret\ndata:\n  password: c2VjcmV0\n",
		err,
		"apply",
	)
	if strings.Contains(doc.Error, "c2VjcmV0") || !strings.Contains(doc.Error, "hidden") {
		t.Fatalf("Secret error was not fail-closed: %q", doc.Error)
	}

	malformed := rejectedPreviewDocument(
		yamlPreviewDocument{},
		"kind: Secret\nstringData: plain-text-secret: broken\n",
		err,
		"apply",
	)
	if strings.Contains(malformed.Error, "c2VjcmV0") {
		t.Fatalf("malformed Secret error leaked: %q", malformed.Error)
	}

	policy := previewDocumentIdentity(0, `apiVersion: kyverno.io/v1
kind: ClusterPolicy
metadata:
  name: generate-secret
spec:
  rules:
  - generate:
      apiVersion: v1
      kind: Secret
`)
	rejectedPolicy := rejectedPreviewDocument(policy, policy.SubmittedYAML, err, "apply")
	if rejectedPolicy.Error != err.Error() {
		t.Fatalf("non-Secret error was hidden: %q", rejectedPolicy.Error)
	}
}

func TestPreviewDocumentIdentityRedactsSubmittedSecret(t *testing.T) {
	doc := previewDocumentIdentity(0, `apiVersion: v1
kind: Secret
metadata:
  name: credentials
  annotations:
    token: annotation-secret
data:
  password: c2VjcmV0
`)
	if !doc.Redacted {
		t.Fatal("submitted Secret was not marked redacted")
	}
	if strings.Contains(doc.SubmittedYAML, "annotation-secret") || strings.Contains(doc.SubmittedYAML, "c2VjcmV0") {
		t.Fatalf("submitted Secret was returned without redaction: %s", doc.SubmittedYAML)
	}
	if !strings.Contains(doc.SubmittedYAML, "<redacted:unchanged>") {
		t.Fatalf("submitted Secret lost useful redacted structure: %s", doc.SubmittedYAML)
	}

	malformed := previewDocumentIdentity(1, "kind: Secret\ndata:\n  token: [raw-secret\n")
	if strings.Contains(malformed.SubmittedYAML, "raw-secret") {
		t.Fatalf("malformed submitted Secret leaked: %s", malformed.SubmittedYAML)
	}
	flow := previewDocumentIdentity(2, `{kind: Secret, data: {token: raw-secret}`)
	if strings.Contains(flow.SubmittedYAML, "raw-secret") {
		t.Fatalf("malformed flow-style Secret leaked: %s", flow.SubmittedYAML)
	}
	crlf := previewDocumentIdentity(3, "kind: Secret\r\ndata:\r\n  token: [raw-secret\r\n")
	if strings.Contains(crlf.SubmittedYAML, "raw-secret") {
		t.Fatalf("malformed CRLF Secret leaked: %s", crlf.SubmittedYAML)
	}
	continued := previewDocumentIdentity(4, "kind:\n  Secret\ndata:\n  token: [raw-secret\n")
	if strings.Contains(continued.SubmittedYAML, "raw-secret") {
		t.Fatalf("malformed continued-kind Secret leaked: %s", continued.SubmittedYAML)
	}
}

func TestPreviewDocumentIdentityDoesNotRedactParsedNonSecret(t *testing.T) {
	manifest := `apiVersion: kyverno.io/v1
kind: ClusterPolicy
metadata:
  name: generate-secret
spec:
  rules:
  - generate:
      apiVersion: v1
      kind: Secret
`
	doc := previewDocumentIdentity(0, manifest)
	if doc.Redacted {
		t.Fatal("parsed non-Secret was incorrectly marked redacted")
	}
	if doc.SubmittedYAML != manifest {
		t.Fatalf("parsed non-Secret YAML was hidden: %q", doc.SubmittedYAML)
	}
}

func TestReadBoundedTextBodyAcceptsBodyWithinLimit(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/", strings.NewReader("kind: ConfigMap\n"))

	body, ok := (&Server{}).readBoundedTextBody(recorder, request, 64)
	if !ok {
		t.Fatalf("readBoundedTextBody() ok = false, want true (status %d)", recorder.Code)
	}
	if body != "kind: ConfigMap\n" {
		t.Fatalf("body = %q, want the request body unchanged", body)
	}
}

// An unbounded io.ReadAll on the apply route is a footgun once a file picker
// feeds it: the caller should learn the body is too large, not have Radar
// buffer it first and then fail with a generic read error.
func TestReadBoundedTextBodyRejectsOversizeBodyWith413(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/", strings.NewReader(strings.Repeat("x", 128)))

	if _, ok := (&Server{}).readBoundedTextBody(recorder, request, 64); ok {
		t.Fatal("readBoundedTextBody() ok = true, want false for an oversize body")
	}
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusRequestEntityTooLarge)
	}
	var response struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("unmarshal error response: %v", err)
	}
	if !strings.Contains(response.Error, "too large") {
		t.Fatalf("error = %q, want it to say the body is too large", response.Error)
	}
}

// A body that is too big is not a malformed request: nothing has parsed the
// YAML at this point, so "invalid preview request" sends the user to debug
// syntax that may be perfectly fine.
func TestHandlePreviewResourcesRejectsOversizeBodyWith413(t *testing.T) {
	prevConn := k8s.GetConnectionStatus()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	t.Cleanup(func() { k8s.SetConnectionStatus(prevConn) })

	oversize := `{"yaml":"` + strings.Repeat("x", maxYAMLPreviewRequestBytes) + `"}`
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/api/resources/preview", strings.NewReader(oversize))

	(&Server{}).handlePreviewResources(recorder, request)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusRequestEntityTooLarge)
	}
	var response struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("unmarshal error response: %v", err)
	}
	if strings.Contains(response.Error, "invalid preview request") {
		t.Fatalf("error = %q, want a size message rather than a malformed-request one", response.Error)
	}
	if !strings.Contains(response.Error, "too large") {
		t.Fatalf("error = %q, want it to say the body is too large", response.Error)
	}
}

// The cap has to hold at the route, not only in the helper: /resources/apply is
// the surface a file picker points at, and the body is the manifest itself.
func TestHandleApplyResourceRejectsOversizeBody(t *testing.T) {
	prevConn := k8s.GetConnectionStatus()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	t.Cleanup(func() { k8s.SetConnectionStatus(prevConn) })

	oversize := strings.Repeat("x", maxYAMLApplyRequestBytes+1)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/api/resources/apply", strings.NewReader(oversize))

	(&Server{}).handleApplyResource(recorder, request)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusRequestEntityTooLarge)
	}
}

// The byte cap alone still admits a small body holding tens of thousands of
// documents. Preview already refuses those; apply refusing them too is what
// stops the two routes disagreeing about the same bundle.
func TestHandleApplyResourceRejectsTooManyDocuments(t *testing.T) {
	prevConn := k8s.GetConnectionStatus()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	t.Cleanup(func() { k8s.SetConnectionStatus(prevConn) })

	doc := "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: n\n"
	body := strings.Repeat(doc+"---\n", maxYAMLApplyDocuments+1)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/api/resources/apply", strings.NewReader(body))

	(&Server{}).handleApplyResource(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	var response struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("unmarshal error response: %v", err)
	}
	if !strings.Contains(response.Error, "at most") {
		t.Fatalf("error = %q, want it to name the document limit", response.Error)
	}
}

func TestApplyDocumentLimitMatchesPreview(t *testing.T) {
	if maxYAMLApplyDocuments != maxYAMLPreviewDocuments {
		t.Fatalf("apply documents = %d, preview documents = %d; the two routes must refuse the same bundles",
			maxYAMLApplyDocuments, maxYAMLPreviewDocuments)
	}
}

// The limit Radar advertises is an amount of YAML. Apply carries it raw, so its
// body cap states it directly; preview carries it JSON-escaped inside an
// envelope, so the envelope must be the looser of the two or preview rejects
// manifests apply would have taken.
//
// Twice is not a margin picked for comfort, it is the ceiling the grammar
// allows. JSON escaping doubles `"` and `\`, and YAML 1.2 admits no raw C0
// control character in content except tab and newline, which also escape to two
// bytes. Every other byte survives encoding unchanged, so no valid YAML document
// can more than double. Anything less leaves manifests that apply accepts and
// preview refuses — a minified JSON blob inside a ConfigMap is nearly half
// quotes and clears a smaller margin easily.
func TestPreviewEnvelopeAbsorbsWorstCaseEscaping(t *testing.T) {
	if maxYAMLPreviewRequestBytes <= 2*maxYAMLContentBytes {
		t.Fatalf("envelope = %d, yaml limit = %d; the envelope must hold the worst-case encoding — twice the content, plus the wrapper around it",
			maxYAMLPreviewRequestBytes, maxYAMLContentBytes)
	}
	if maxYAMLApplyRequestBytes != maxYAMLContentBytes {
		t.Fatalf("apply body = %d, yaml limit = %d; apply carries the YAML raw and must state the same limit",
			maxYAMLApplyRequestBytes, maxYAMLContentBytes)
	}
}

// The boundary the reviewer asked for: a file at the advertised limit has to
// survive review, not 413 somewhere short of it because of escaping.
func TestPreviewAcceptsYAMLAtTheAdvertisedLimit(t *testing.T) {
	prevConn := k8s.GetConnectionStatus()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	t.Cleanup(func() { k8s.SetConnectionStatus(prevConn) })

	// Real manifest shape, padded to exactly the limit, with the newlines that
	// make the encoded envelope larger than the document.
	head := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: big\ndata:\n  payload: "
	doc := head + strings.Repeat("a\n    ", (maxYAMLContentBytes-len(head))/6)
	doc += strings.Repeat("a", maxYAMLContentBytes-len(doc))

	body, err := json.Marshal(yamlPreviewRequest{YAML: doc, Mode: "apply"})
	if err != nil {
		t.Fatalf("marshal preview request: %v", err)
	}
	if len(body) <= maxYAMLContentBytes {
		t.Fatalf("encoded body = %d bytes, want it to exceed the %d byte YAML limit — otherwise this proves nothing",
			len(body), maxYAMLContentBytes)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/api/resources/preview", bytes.NewReader(body))
	(&Server{}).handlePreviewResources(recorder, request)

	if recorder.Code == http.StatusRequestEntityTooLarge {
		t.Fatalf("status = 413 for a document at the advertised limit: %s", recorder.Body.String())
	}
}

func TestPreviewRejectsYAMLOverTheLimitDespiteAFittingEnvelope(t *testing.T) {
	prevConn := k8s.GetConnectionStatus()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	t.Cleanup(func() { k8s.SetConnectionStatus(prevConn) })

	// Plain text, so the envelope stays well under its own bound while the
	// document itself is over the limit — the case only a decoded check catches.
	oversize := strings.Repeat("a", maxYAMLContentBytes+1)
	body, err := json.Marshal(yamlPreviewRequest{YAML: oversize, Mode: "apply"})
	if err != nil {
		t.Fatalf("marshal preview request: %v", err)
	}
	if len(body) > maxYAMLPreviewRequestBytes {
		t.Fatalf("envelope = %d bytes, over its own bound; this test would pass for the wrong reason", len(body))
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/api/resources/preview", bytes.NewReader(body))
	(&Server{}).handlePreviewResources(recorder, request)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusRequestEntityTooLarge)
	}
}

// The bound above, exercised rather than asserted: a document at the limit made
// entirely of characters that double under JSON encoding.
func TestPreviewAcceptsFullyEscapedYAMLAtTheLimit(t *testing.T) {
	prevConn := k8s.GetConnectionStatus()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	t.Cleanup(func() { k8s.SetConnectionStatus(prevConn) })

	worstCase := strings.Repeat(`"`, maxYAMLContentBytes)
	body, err := json.Marshal(yamlPreviewRequest{YAML: worstCase, Mode: "apply"})
	if err != nil {
		t.Fatalf("marshal preview request: %v", err)
	}
	t.Logf("encoded body = %d bytes against a %d byte envelope", len(body), maxYAMLPreviewRequestBytes)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/api/resources/preview", bytes.NewReader(body))
	(&Server{}).handlePreviewResources(recorder, request)

	// It is not valid YAML and will be refused on its content, but never for
	// its size — the envelope has to carry it that far.
	if recorder.Code == http.StatusRequestEntityTooLarge {
		t.Fatalf("status = 413 for a document at the limit whose encoding doubled it: %s", recorder.Body.String())
	}
}
