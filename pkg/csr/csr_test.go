package csr

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestStatusCrossLanguage(t *testing.T) {
	raw, err := os.ReadFile("testdata/states.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Vectors []struct {
			Name               string         `json:"name"`
			CSR                map[string]any `json:"csr"`
			State              State          `json:"state"`
			Label              string         `json:"label"`
			Level              string         `json:"level"`
			ConditionType      string         `json:"conditionType"`
			CertificatePresent bool           `json:"certificatePresent"`
		} `json:"vectors"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Vectors) == 0 {
		t.Fatal("no vectors")
	}
	for _, v := range fixture.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			got := StatusOf(v.CSR)
			conditionType := ""
			if got.Condition != nil {
				conditionType = got.Condition.Type
			}
			if got.State != v.State || got.State.Label() != v.Label || string(got.Level(v.CSR, time.Now())) != v.Level ||
				conditionType != v.ConditionType || got.CertificatePresent != v.CertificatePresent {
				t.Fatalf("got state=%s label=%q level=%s condition=%q certificate=%v; want state=%s label=%q level=%s condition=%q certificate=%v",
					got.State, got.State.Label(), got.Level(v.CSR, time.Now()), conditionType, got.CertificatePresent,
					v.State, v.Label, v.Level, v.ConditionType, v.CertificatePresent)
			}
		})
	}
}

func TestStatusOfKeepsTheDecidingCondition(t *testing.T) {
	got := StatusOf(map[string]any{"status": map[string]any{"conditions": []any{
		map[string]any{"type": "Approved", "status": "True", "reason": "KubectlApprove"},
		map[string]any{
			"type": "Failed", "status": "True", "reason": "SignerValidationFailure", "message": "invalid usage: server auth",
			"lastUpdateTime": "2026-10-20T10:00:05Z", "lastTransitionTime": "2026-10-20T10:00:04Z",
		},
	}}})
	want := &Condition{
		Type: "Failed", Reason: "SignerValidationFailure", Message: "invalid usage: server auth",
		LastUpdateTime: "2026-10-20T10:00:05Z", LastTransitionTime: "2026-10-20T10:00:04Z",
	}
	if got.State != StateFailed || !reflect.DeepEqual(got.Condition, want) {
		t.Fatalf("got %+v (%+v); want failed with %+v", got, got.Condition, want)
	}
}

func TestIsObject(t *testing.T) {
	for _, tc := range []struct {
		apiVersion, kind string
		want             bool
	}{
		{"certificates.k8s.io/v1", "CertificateSigningRequest", true},
		{"certificates.k8s.io/v1beta1", "CertificateSigningRequest", true},
		{"example.com/v1", "CertificateSigningRequest", false},
		{"cert-manager.io/v1", "CertificateRequest", false},
		{"certificates.k8s.io/v1", "PodCertificateRequest", false},
	} {
		if got := IsObject(map[string]any{"apiVersion": tc.apiVersion, "kind": tc.kind}); got != tc.want {
			t.Errorf("IsObject(%s %s) = %v, want %v", tc.apiVersion, tc.kind, got, tc.want)
		}
	}
}

func TestDescribeDecodesTheRequestedIdentity(t *testing.T) {
	key := newKey(t)
	details := Describe(object(b64(requestPEM(t, key, nodeRequest())), nil), time.Now())

	want := Request{
		Status:             DecodeDecoded,
		Subject:            "CN=system:node:worker-1,O=system:nodes",
		CommonName:         "system:node:worker-1",
		Organizations:      []string{"system:nodes"},
		DNSNames:           []string{"worker-1.example.internal"},
		IPAddresses:        []string{"10.0.0.12"},
		EmailAddresses:     []string{"ops@example.com"},
		URIs:               []string{"spiffe://cluster.local/ns/default/sa/app"},
		PublicKey:          "EC P-256",
		SignatureAlgorithm: "ECDSA-SHA256",
	}
	if !reflect.DeepEqual(details.Request, want) {
		t.Fatalf("request = %+v\nwant %+v", details.Request, want)
	}
	if details.Certificate.Status != DecodeAbsent {
		t.Fatalf("certificate status = %s, want absent", details.Certificate.Status)
	}
}

func TestDescribeFlagsSANsOfAnUnreadableType(t *testing.T) {
	key := newKey(t)
	upnOnly := &x509.CertificateRequest{
		Subject:         pkix.Name{CommonName: "alice"},
		ExtraExtensions: []pkix.Extension{upnSANExtension(t, "alice@corp.example")},
	}
	got := Describe(object(b64(requestPEM(t, key, upnOnly)), nil), time.Now()).Request
	if got.Status != DecodeDecoded || !got.UnreadableSANs {
		t.Fatalf("UPN-only request: status %s, unreadableSANs %v; want decoded, true", got.Status, got.UnreadableSANs)
	}

	// A readable name alongside the UPN lists the readable one and says nothing more.
	withDNS := &x509.CertificateRequest{
		Subject:         pkix.Name{CommonName: "alice"},
		ExtraExtensions: []pkix.Extension{upnAndDNSSANExtension(t, "alice@corp.example", "alice.example.internal")},
	}
	got = Describe(object(b64(requestPEM(t, key, withDNS)), nil), time.Now()).Request
	if got.UnreadableSANs || !reflect.DeepEqual(got.DNSNames, []string{"alice.example.internal"}) {
		t.Fatalf("UPN+DNS request: unreadableSANs %v, dnsNames %v; want false, [alice.example.internal]", got.UnreadableSANs, got.DNSNames)
	}

	noSANs := &x509.CertificateRequest{Subject: pkix.Name{CommonName: "alice"}}
	got = Describe(object(b64(requestPEM(t, key, noSANs)), nil), time.Now()).Request
	if got.UnreadableSANs {
		t.Fatal("request without a SAN extension was flagged as having unreadable SANs")
	}
}

// upnSANExtension is a subjectAltName holding one Microsoft UPN otherName,
// a type Go's parser skips.
func upnSANExtension(t *testing.T, upn string) pkix.Extension {
	return sanExtension(t, upnOtherName(t, upn))
}

func upnAndDNSSANExtension(t *testing.T, upn, dns string) pkix.Extension {
	return sanExtension(t, upnOtherName(t, upn), asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 2, Bytes: []byte(dns)})
}

func upnOtherName(t *testing.T, upn string) asn1.RawValue {
	t.Helper()
	value, err := asn1.MarshalWithParams(upn, "utf8")
	if err != nil {
		t.Fatal(err)
	}
	explicit, err := asn1.Marshal(asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: value})
	if err != nil {
		t.Fatal(err)
	}
	typeID, err := asn1.Marshal(asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 311, 20, 2, 3})
	if err != nil {
		t.Fatal(err)
	}
	return asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: append(typeID, explicit...)}
}

func sanExtension(t *testing.T, names ...asn1.RawValue) pkix.Extension {
	t.Helper()
	der, err := asn1.Marshal(names)
	if err != nil {
		t.Fatal(err)
	}
	return pkix.Extension{Id: asn1.ObjectIdentifier{2, 5, 29, 17}, Value: der}
}

func TestDescribeReportsARequestThatIsNotSignedByItsKey(t *testing.T) {
	block, _ := pem.Decode(requestPEM(t, newKey(t), nodeRequest()))
	block.Bytes[len(block.Bytes)-1] ^= 0xff
	got := Describe(object(b64(pem.EncodeToMemory(block)), nil), time.Now()).Request
	if got.Status != DecodeDecoded || got.CommonName != "system:node:worker-1" || got.SignatureError == "" {
		t.Fatalf("got %+v; want the identity decoded with a signature error", got)
	}
}

func TestDescribeReportsUnreadableRequests(t *testing.T) {
	certificate, _ := newCertificate(t, pkix.Name{CommonName: "not-a-request"}, newKey(t), nil, nil)
	for _, tc := range []struct {
		name    string
		request any
		status  DecodeStatus
		err     string
	}{
		{name: "missing", request: nil, status: DecodeAbsent},
		{name: "empty", request: "", status: DecodeAbsent},
		{name: "not a string", request: int64(42), status: DecodeInvalid, err: "spec.request is not a string"},
		{name: "not base64", request: "%%%", status: DecodeInvalid, err: "spec.request is not valid base64"},
		{name: "no PEM", request: b64([]byte("hello")), status: DecodeInvalid, err: "spec.request holds no PEM block"},
		{name: "wrong PEM type", request: b64(pemBlock("CERTIFICATE", certificate.Raw)), status: DecodeInvalid, err: "spec.request holds a CERTIFICATE PEM block, not a CERTIFICATE REQUEST"},
		{name: "malformed DER", request: b64(pemBlock("CERTIFICATE REQUEST", []byte("not der"))), status: DecodeInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Describe(object(tc.request, nil), time.Now()).Request
			if got.Status != tc.status || !strings.Contains(got.Error, tc.err) || (tc.status == DecodeInvalid) != (got.Error != "") {
				t.Fatalf("got %+v; want status %s with error containing %q", got, tc.status, tc.err)
			}
			if got.Subject != "" || got.CommonName != "" || got.PublicKey != "" {
				t.Fatalf("an unread request must not carry identity fields: %+v", got)
			}
		})
	}
}

func TestDescribeDecodesIssuedCertificates(t *testing.T) {
	caKey := newKey(t)
	ca, caPEM := newCertificate(t, pkix.Name{CommonName: "radar-test-ca"}, caKey, nil, nil)
	leaf, leafPEM := newCertificate(t, pkix.Name{CommonName: "system:node:worker-1", Organization: []string{"system:nodes"}}, newKey(t), ca, caKey)

	got := Describe(object(nil, b64(append(append([]byte{}, leafPEM...), caPEM...))), time.Now()).Certificate
	if got.Status != DecodeDecoded || len(got.Problems) != 0 || len(got.Certificates) != 2 {
		t.Fatalf("got %+v; want two decoded certificates", got)
	}
	issued := got.Certificates[0]
	if issued.Subject != "system:node:worker-1" || issued.SubjectDN != "CN=system:node:worker-1,O=system:nodes" ||
		issued.Issuer != "radar-test-ca" || issued.SerialNumber != strings.ToUpper(leaf.SerialNumber.Text(16)) || issued.Expired {
		t.Fatalf("leaf = %+v", issued)
	}
	if got.Certificates[1].Subject != "radar-test-ca" || !got.Certificates[1].SelfSigned {
		t.Fatalf("CA = %+v", got.Certificates[1])
	}
}

func TestDescribeNeverReportsUnreadableCertificateDataAsAbsent(t *testing.T) {
	_, leafPEM := newCertificate(t, pkix.Name{CommonName: "system:node:worker-1"}, newKey(t), nil, nil)
	leafDER, _ := pem.Decode(leafPEM)
	requestBlock := pemBlock("CERTIFICATE REQUEST", requestDER(t, newKey(t), nodeRequest()))
	brokenBase64 := []byte("-----BEGIN CERTIFICATE-----\n!!!!\n-----END CERTIFICATE-----\n")
	notACertificate := pemBlock("CERTIFICATE", []byte("not der"))

	for _, tc := range []struct {
		name        string
		certificate any
		status      DecodeStatus
		read        int
		problems    []string
	}{
		{name: "missing", certificate: nil, status: DecodeAbsent},
		{name: "empty", certificate: "", status: DecodeAbsent},
		{name: "not a string", certificate: true, status: DecodeInvalid, problems: []string{"status.certificate is not a string"}},
		{name: "not base64", certificate: "%%%", status: DecodeInvalid, problems: []string{"status.certificate is not valid base64"}},
		{name: "whitespace", certificate: b64([]byte("\n  \n")), status: DecodeInvalid, problems: []string{"status.certificate holds no PEM block"}},
		{name: "DER without PEM armor", certificate: b64(leafDER.Bytes), status: DecodeInvalid, problems: []string{"status.certificate contains data outside PEM blocks"}},
		{name: "only a malformed certificate", certificate: b64(notACertificate), status: DecodeInvalid, problems: []string{"PEM block 1 is not a valid certificate: x509:"}},
		{name: "only an undecodable block", certificate: b64(brokenBase64), status: DecodeInvalid, problems: []string{"PEM block 1 could not be decoded"}},
		{name: "only a request block", certificate: b64(requestBlock), status: DecodeInvalid, problems: []string{"PEM block 1 is a CERTIFICATE REQUEST, not a certificate"}},
		{name: "certificate then malformed certificate", certificate: b64(join(leafPEM, notACertificate)), status: DecodePartial, read: 1, problems: []string{"PEM block 2 is not a valid certificate: x509:"}},
		{name: "undecodable block then certificate", certificate: b64(join(brokenBase64, leafPEM)), status: DecodePartial, read: 1, problems: []string{"PEM block 1 could not be decoded"}},
		{name: "certificate then request block", certificate: b64(join(leafPEM, requestBlock)), status: DecodePartial, read: 1, problems: []string{"PEM block 2 is a CERTIFICATE REQUEST, not a certificate"}},
		{name: "unterminated block then certificate", certificate: b64(join([]byte("-----BEGIN CERTIFICATE-----\nbm90IGRlcg==\n"), leafPEM)), status: DecodePartial, read: 1, problems: []string{"PEM block 1 has no END line"}},
		{name: "text before the certificate", certificate: b64(join([]byte("Certificate:\n    Data: ...\n"), leafPEM)), status: DecodePartial, read: 1, problems: []string{"status.certificate contains data outside PEM blocks"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Describe(object(nil, tc.certificate), time.Now()).Certificate
			if got.Status != tc.status || len(got.Certificates) != tc.read || len(got.Problems) != len(tc.problems) {
				t.Fatalf("got %+v; want status %s with %d certificate(s) and problems %q", got, tc.status, tc.read, tc.problems)
			}
			for i, want := range tc.problems {
				if !strings.HasPrefix(got.Problems[i], want) {
					t.Errorf("problem %d = %q, want prefix %q", i, got.Problems[i], want)
				}
			}
		})
	}
}

func nodeRequest() *x509.CertificateRequest {
	return &x509.CertificateRequest{
		Subject:        pkix.Name{CommonName: "system:node:worker-1", Organization: []string{"system:nodes"}},
		DNSNames:       []string{"worker-1.example.internal"},
		IPAddresses:    []net.IP{net.ParseIP("10.0.0.12")},
		EmailAddresses: []string{"ops@example.com"},
		URIs:           []*url.URL{{Scheme: "spiffe", Host: "cluster.local", Path: "/ns/default/sa/app"}},
	}
}

func newKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func requestDER(t *testing.T, key *ecdsa.PrivateKey, template *x509.CertificateRequest) []byte {
	t.Helper()
	der, err := x509.CreateCertificateRequest(rand.Reader, template, key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func requestPEM(t *testing.T, key *ecdsa.PrivateKey, template *x509.CertificateRequest) []byte {
	return pemBlock("CERTIFICATE REQUEST", requestDER(t, key, template))
}

// newCertificate self-signs when parent is nil.
func newCertificate(t *testing.T, subject pkix.Name, key *ecdsa.PrivateKey, parent *x509.Certificate, parentKey *ecdsa.PrivateKey) (*x509.Certificate, []byte) {
	t.Helper()
	serial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               subject,
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		BasicConstraintsValid: true,
		IsCA:                  parent == nil,
	}
	if parent == nil {
		parent, parentKey = template, key
	}
	der, err := x509.CreateCertificate(rand.Reader, template, parent, &key.PublicKey, parentKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, pemBlock("CERTIFICATE", der)
}

func object(request, certificate any) map[string]any {
	spec := map[string]any{}
	status := map[string]any{}
	if request != nil {
		spec["request"] = request
	}
	if certificate != nil {
		status["certificate"] = certificate
	}
	return map[string]any{"apiVersion": "certificates.k8s.io/v1", "kind": Kind, "spec": spec, "status": status}
}

func pemBlock(blockType string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der})
}

func join(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func b64(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}
