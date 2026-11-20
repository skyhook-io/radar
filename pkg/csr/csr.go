// Package csr reads Kubernetes CertificateSigningRequests
// (certificates.k8s.io/v1): where a request stands between approval and
// issuance, and what its PKCS#10 request and returned certificate contain.
//
// The identity in a PKCS#10 request is whatever its creator asked for. It is
// not evidence of what an approver authorized or a signer issued.
package csr

import (
	"bytes"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"time"

	"github.com/skyhook-io/radar/pkg/health"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/topology"
)

const (
	Group = "certificates.k8s.io"
	Kind  = "CertificateSigningRequest"
)

// IsObject reports whether obj is a certificates.k8s.io CertificateSigningRequest.
func IsObject(obj map[string]any) bool {
	kind, _ := obj["kind"].(string)
	apiVersion, _ := obj["apiVersion"].(string)
	return kind == Kind && resourceid.GroupFromAPIVersion(apiVersion) == Group
}

// State is where a request stands, read the way kubectl and client-go's
// csr.WaitForCertificate (what the kubelet waits with) read it: Denied and
// Failed are final, and a returned certificate counts only once the request is
// Approved.
type State string

const (
	StatePending  State = "pending"
	StateApproved State = "approved"
	StateIssued   State = "issued"
	StateDenied   State = "denied"
	StateFailed   State = "failed"
)

// Label is the state as the UI's status badge shows it.
func (s State) Label() string {
	switch s {
	case StatePending:
		return "Pending approval"
	case StateApproved:
		return "Approved, not issued"
	case StateIssued:
		return "Issued"
	case StateDenied:
		return "Denied"
	case StateFailed:
		return "Failed"
	}
	return string(s)
}

// waitingStuckAfter matches the UI's PENDING_STUCK_MINUTES for Pods.
const waitingStuckAfter = 5 * time.Minute

// Level maps the request onto Radar's health vocabulary. Waiting for an
// approver or a signer reads like a pending Pod: neutral at first, degraded
// once it has waited waitingStuckAfter, counted from creation while pending
// and from approval once approved. A missing time counts as no wait.
func (s Status) Level(obj map[string]any, now time.Time) health.Level {
	switch s.State {
	case StateIssued:
		return health.LevelHealthy
	case StateDenied, StateFailed:
		return health.LevelUnhealthy
	case StatePending, StateApproved:
		since := ""
		if s.State == StatePending {
			metadata, _ := obj["metadata"].(map[string]any)
			since, _ = metadata["creationTimestamp"].(string)
		} else if s.Condition != nil {
			since = s.Condition.LastUpdateTime
			if since == "" {
				since = s.Condition.LastTransitionTime
			}
		}
		if t, err := time.Parse(time.RFC3339, since); err == nil && now.Sub(t) >= waitingStuckAfter {
			return health.LevelDegraded
		}
		return health.LevelNeutral
	}
	return health.LevelUnknown
}

// Condition is one of a request's Approved, Denied or Failed conditions.
type Condition struct {
	Type               string `json:"type"`
	Reason             string `json:"reason,omitempty"`
	Message            string `json:"message,omitempty"`
	LastUpdateTime     string `json:"lastUpdateTime,omitempty"`
	LastTransitionTime string `json:"lastTransitionTime,omitempty"`
}

// Status is a request's state and the condition that decided it.
type Status struct {
	State State
	// Condition decided State: Denied, Failed or Approved. Nil while pending.
	Condition *Condition
	// CertificatePresent reports a non-empty status.certificate whatever the
	// state; one on a request that is not Approved is not issued.
	CertificatePresent bool
}

// StatusOf reads a request's state from its conditions and certificate.
func StatusOf(obj map[string]any) Status {
	status, _ := obj["status"].(map[string]any)
	conditions, _ := status["conditions"].([]any)
	var approved, denied, failed *Condition
	for _, raw := range conditions {
		fields, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		cond := Condition{
			Type:               stringField(fields, "type"),
			Reason:             stringField(fields, "reason"),
			Message:            stringField(fields, "message"),
			LastUpdateTime:     stringField(fields, "lastUpdateTime"),
			LastTransitionTime: stringField(fields, "lastTransitionTime"),
		}
		// The API admits only status True for these types, and kubectl and
		// client-go read the type alone.
		switch cond.Type {
		case "Approved":
			if approved == nil {
				approved = &cond
			}
		case "Denied":
			if denied == nil {
				denied = &cond
			}
		case "Failed":
			if failed == nil {
				failed = &cond
			}
		}
	}

	out := Status{CertificatePresent: status["certificate"] != nil && status["certificate"] != ""}
	switch {
	case denied != nil:
		out.State, out.Condition = StateDenied, denied
	case failed != nil:
		out.State, out.Condition = StateFailed, failed
	case approved != nil && out.CertificatePresent:
		out.State, out.Condition = StateIssued, approved
	case approved != nil:
		out.State, out.Condition = StateApproved, approved
	default:
		out.State = StatePending
	}
	return out
}

// DecodeStatus says how much of a payload Radar could read.
type DecodeStatus string

const (
	// DecodeAbsent: the field is missing or empty.
	DecodeAbsent DecodeStatus = "absent"
	// DecodeDecoded: all of it was read.
	DecodeDecoded DecodeStatus = "decoded"
	// DecodePartial: some certificates were read and some of the data was not.
	DecodePartial DecodeStatus = "partial"
	// DecodeInvalid: the field holds data that could not be read.
	DecodeInvalid DecodeStatus = "invalid"
)

// Details is what a request's spec.request and status.certificate contain.
// Data that could not be read is reported as such, never as absent.
type Details struct {
	Request     Request     `json:"request"`
	Certificate Certificate `json:"certificate"`
}

// Request is the PKCS#10 request: the identity and key its creator asked a
// certificate for.
type Request struct {
	Status DecodeStatus `json:"status"`
	// Error says why the request could not be read.
	Error          string   `json:"error,omitempty"`
	Subject        string   `json:"subject,omitempty"`
	CommonName     string   `json:"commonName,omitempty"`
	Organizations  []string `json:"organizations,omitempty"`
	DNSNames       []string `json:"dnsNames,omitempty"`
	IPAddresses    []string `json:"ipAddresses,omitempty"`
	EmailAddresses []string `json:"emailAddresses,omitempty"`
	URIs           []string `json:"uris,omitempty"`
	// UnreadableSANs is set when the request has a subject alternative name
	// extension but none of its names is a DNS name, IP address, email
	// address or URI, such as a Microsoft UPN. Without it the request would
	// read as asking for no SANs.
	UnreadableSANs     bool   `json:"unreadableSANs,omitempty"`
	PublicKey          string `json:"publicKey,omitempty"`
	SignatureAlgorithm string `json:"signatureAlgorithm,omitempty"`
	// SignatureError is set when the request parses but is not signed by its
	// own key.
	SignatureError string `json:"signatureError,omitempty"`
}

// Certificate is what the signer returned in status.certificate.
type Certificate struct {
	Status       DecodeStatus        `json:"status"`
	Certificates []IssuedCertificate `json:"certificates,omitempty"`
	// Problems names each part of the data that is not a readable certificate.
	Problems []string `json:"problems,omitempty"`
}

// IssuedCertificate is a returned certificate, with the parts of its identity
// topology.CertificateInfo leaves out.
type IssuedCertificate struct {
	topology.CertificateInfo
	// SubjectDN is the whole subject. CertificateInfo.Subject keeps only the
	// common name, dropping the organizations Kubernetes reads as groups.
	SubjectDN      string   `json:"subjectDN,omitempty"`
	EmailAddresses []string `json:"emailAddresses,omitempty"`
	URIs           []string `json:"uris,omitempty"`
}

// Describe decodes a request's spec.request and status.certificate.
func Describe(obj map[string]any, now time.Time) Details {
	spec, _ := obj["spec"].(map[string]any)
	status, _ := obj["status"].(map[string]any)
	return Details{
		Request:     decodeRequest(spec["request"]),
		Certificate: decodeCertificate(status["certificate"], now),
	}
}

func decodeRequest(value any) Request {
	raw, err := payload(value, "spec.request")
	if err != nil {
		return Request{Status: DecodeInvalid, Error: err.Error()}
	}
	if raw == nil {
		return Request{Status: DecodeAbsent}
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return Request{Status: DecodeInvalid, Error: "spec.request holds no PEM block"}
	}
	if block.Type != "CERTIFICATE REQUEST" {
		return Request{Status: DecodeInvalid, Error: fmt.Sprintf("spec.request holds a %s PEM block, not a CERTIFICATE REQUEST", block.Type)}
	}
	req, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return Request{Status: DecodeInvalid, Error: err.Error()}
	}

	// Go leaves PublicKey nil for an algorithm it does not know.
	publicKey := "unrecognized algorithm"
	if req.PublicKey != nil {
		publicKey = topology.PublicKeyType(req.PublicKey)
	}
	out := Request{
		Status:             DecodeDecoded,
		Subject:            req.Subject.String(),
		CommonName:         req.Subject.CommonName,
		Organizations:      req.Subject.Organization,
		DNSNames:           req.DNSNames,
		IPAddresses:        ipStrings(req.IPAddresses),
		EmailAddresses:     req.EmailAddresses,
		URIs:               uriStrings(req.URIs),
		PublicKey:          publicKey,
		SignatureAlgorithm: req.SignatureAlgorithm.String(),
	}
	out.UnreadableSANs = hasExtension(req.Extensions, oidSubjectAltName) &&
		len(out.DNSNames)+len(out.IPAddresses)+len(out.EmailAddresses)+len(out.URIs) == 0
	if err := req.CheckSignature(); err != nil {
		out.SignatureError = err.Error()
	}
	return out
}

var oidSubjectAltName = asn1.ObjectIdentifier{2, 5, 29, 17}

func hasExtension(extensions []pkix.Extension, oid asn1.ObjectIdentifier) bool {
	for _, ext := range extensions {
		if ext.Id.Equal(oid) {
			return true
		}
	}
	return false
}

// pemBlockPattern finds each armored block, readable or not, so a block that
// fails to decode is reported rather than skipped the way pem.Decode skips it.
var pemBlockPattern = regexp.MustCompile(`(?s)-----BEGIN [^\n]*?-----.*?-----END [^\n]*?-----`)

func decodeCertificate(value any, now time.Time) Certificate {
	raw, err := payload(value, "status.certificate")
	if err != nil {
		return Certificate{Status: DecodeInvalid, Problems: []string{err.Error()}}
	}
	if raw == nil {
		return Certificate{Status: DecodeAbsent}
	}

	var out Certificate
	outside := false
	end := 0
	for i, span := range pemBlockPattern.FindAllIndex(raw, -1) {
		if len(bytes.TrimSpace(raw[end:span[0]])) > 0 {
			outside = true
		}
		end = span[1]
		// A BEGIN with no END of its own pulls the next block into this span.
		// pem.Decode skips the unterminated part, so report it here.
		if bytes.Count(raw[span[0]:span[1]], []byte("-----BEGIN ")) > 1 {
			out.Problems = append(out.Problems, fmt.Sprintf("PEM block %d has no END line", i+1))
		}
		block, _ := pem.Decode(raw[span[0]:span[1]])
		switch {
		case block == nil:
			out.Problems = append(out.Problems, fmt.Sprintf("PEM block %d could not be decoded", i+1))
		case block.Type != "CERTIFICATE":
			out.Problems = append(out.Problems, fmt.Sprintf("PEM block %d is a %s, not a certificate", i+1, block.Type))
		default:
			certs, err := x509.ParseCertificates(block.Bytes)
			if err != nil {
				out.Problems = append(out.Problems, fmt.Sprintf("PEM block %d is not a valid certificate: %v", i+1, err))
				continue
			}
			for _, cert := range certs {
				out.Certificates = append(out.Certificates, IssuedCertificate{
					CertificateInfo: topology.NewCertificateInfo(cert, now),
					SubjectDN:       cert.Subject.String(),
					EmailAddresses:  cert.EmailAddresses,
					URIs:            uriStrings(cert.URIs),
				})
			}
		}
	}
	if outside || len(bytes.TrimSpace(raw[end:])) > 0 {
		out.Problems = append(out.Problems, "status.certificate contains data outside PEM blocks")
	}

	switch {
	case len(out.Certificates) == 0:
		out.Status = DecodeInvalid
		if len(out.Problems) == 0 {
			out.Problems = []string{"status.certificate holds no PEM block"}
		}
	case len(out.Problems) > 0:
		out.Status = DecodePartial
	default:
		out.Status = DecodeDecoded
	}
	return out
}

// payload decodes a []byte field, which the API serves as base64. A nil result
// with no error means the field is missing or empty.
func payload(value any, field string) ([]byte, error) {
	if value == nil {
		return nil, nil
	}
	s, ok := value.(string)
	if !ok {
		return nil, fmt.Errorf("%s is not a string", field)
	}
	if s == "" {
		return nil, nil
	}
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("%s is not valid base64", field)
	}
	return raw, nil
}

func stringField(fields map[string]any, key string) string {
	s, _ := fields[key].(string)
	return s
}

func ipStrings(ips []net.IP) []string {
	var out []string
	for _, ip := range ips {
		out = append(out, ip.String())
	}
	return out
}

func uriStrings(uris []*url.URL) []string {
	var out []string
	for _, u := range uris {
		out = append(out, u.String())
	}
	return out
}
