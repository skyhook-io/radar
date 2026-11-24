package cloud

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	enrollmentChallengePath = "/api/agent/enrollment-challenge"
	maxEnrollmentKeyBytes   = 4 << 10
	maxChallengeBytes       = 2 << 10
	maxProofHeaderBytes     = 2 << 10
)

var enrollmentOrganizationPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func ValidateEnrollmentOrganization(organization string) error {
	if !enrollmentOrganizationPattern.MatchString(organization) {
		return errors.New("enrollment organization must be the organization ID from Hub setup, or self-hosted for a configured self-hosted install key")
	}
	return nil
}

func NormalizeEnrollmentOrigin(raw string) (string, error) {
	if len(raw) > 512 || strings.ContainsFunc(raw, func(r rune) bool { return r <= ' ' || r > '~' }) {
		return "", errors.New("Hub enrollment address must use ASCII characters; use a punycode hostname for an international domain")
	}
	origin, err := NormalizeHubOrigin(raw)
	if err != nil {
		return "", err
	}
	return enrollmentHubOrigin(origin), nil
}

func ReadEnrollmentPrivateKey(path string) (ed25519.PrivateKey, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read enrollment private-key file: %w", err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxEnrollmentKeyBytes+1))
	if err != nil {
		return nil, errors.New("could not read enrollment private-key file")
	}
	if len(raw) > maxEnrollmentKeyBytes {
		return nil, errors.New("enrollment private-key file exceeds 4 KiB")
	}
	trimmed := bytes.TrimSpace(raw)
	if !bytes.HasPrefix(trimmed, []byte("-----BEGIN PRIVATE KEY-----")) || bytes.Count(trimmed, []byte("-----BEGIN ")) != 1 {
		return nil, errors.New("enrollment needs one unencrypted PKCS#8 Ed25519 PRIVATE KEY PEM file")
	}
	block, rest := pem.Decode(trimmed)
	if block == nil || block.Type != "PRIVATE KEY" || len(block.Headers) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("enrollment needs one unencrypted PKCS#8 Ed25519 PRIVATE KEY PEM file")
	}
	var outer asn1.RawValue
	if rest, err := asn1.Unmarshal(block.Bytes, &outer); err != nil || len(rest) != 0 {
		return nil, errors.New("enrollment private-key file is not valid PKCS#8")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("enrollment private-key file is not valid PKCS#8")
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok || len(key) != ed25519.PrivateKeySize {
		return nil, errors.New("enrollment private-key file must contain an Ed25519 key")
	}
	return key, nil
}

func enrollmentPublicSelector(key ed25519.PrivateKey) string {
	digest := sha256.Sum256(key[ed25519.SeedSize:])
	return "rhpk_" + base64.RawURLEncoding.EncodeToString(digest[:])
}

func enrollmentSigningMessage(origin, organization, selector, challenge string, body []byte) []byte {
	digest := sha256.Sum256(body)
	return []byte(strings.Join([]string{
		"radar-hub-enrollment-v1", origin, organization, selector, challenge,
		base64.RawURLEncoding.EncodeToString(digest[:]),
	}, "\n"))
}

func NewSignedEnrollClient(hubOrigin, privateKeyFile, organization string, ca *x509.CertPool) (*EnrollClient, error) {
	if err := ValidateEnrollmentOrganization(organization); err != nil {
		return nil, err
	}
	origin, err := NormalizeEnrollmentOrigin(hubOrigin)
	if err != nil {
		return nil, err
	}
	if privateKeyFile == "" {
		return nil, errors.New("enrollment private-key file is required")
	}
	c, err := NewEnrollClient(origin, "", ca)
	if err != nil {
		return nil, err
	}
	c.PrivateKeyFile, c.Organization = privateKeyFile, organization
	return c, nil
}

func (c *EnrollClient) signedAuthorization(ctx context.Context, body []byte) (string, error) {
	if c.KeyFile != "" {
		return "", errors.New("private-key enrollment cannot also use an enrollment secret")
	}
	if err := ValidateEnrollmentOrganization(c.Organization); err != nil {
		return "", err
	}
	key, err := ReadEnrollmentPrivateKey(c.PrivateKeyFile)
	if err != nil {
		return "", err
	}
	challengeReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.HubOrigin+enrollmentChallengePath, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.noRedirectClient().Do(challengeReq)
	if err != nil {
		return "", &HubUnreachableError{HubBase: c.HubOrigin, Err: err}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxChallengeBytes+1))
	if err != nil || len(raw) > maxChallengeBytes {
		return "", errors.New("could not read a bounded enrollment challenge from Hub")
	}
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
			return "", &EnrollError{StatusCode: resp.StatusCode, Code: "public_key_enrollment_unavailable", Message: "Public-key enrollment is unavailable at this Hub address. Check the Hub version, rollout setting and API address."}
		}
		var reply enrollResponse
		_ = json.Unmarshal(raw, &reply)
		retryAfter, hasRetryAfter := parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		return "", &EnrollError{StatusCode: resp.StatusCode, Code: truncateRunes(sanitizeConnectErrorText(reply.Error), 64), Message: truncateRunes(sanitizeConnectErrorText(reply.Message), maxEnrollMessageRunes), RetryAfter: retryAfter, HasRetryAfter: hasRetryAfter}
	}
	var reply struct {
		Challenge string `json:"challenge"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		return "", errors.New("Hub returned an invalid enrollment challenge")
	}
	payload, mac, ok := strings.Cut(reply.Challenge, ".")
	payloadBytes, payloadErr := base64.RawURLEncoding.Strict().DecodeString(payload)
	macBytes, macErr := base64.RawURLEncoding.Strict().DecodeString(mac)
	_, timeErr := time.Parse(time.RFC3339, reply.ExpiresAt)
	if !ok || payloadErr != nil || macErr != nil || len(payloadBytes) == 0 || len(payloadBytes) > 1024 || len(macBytes) != sha256.Size || base64.RawURLEncoding.EncodeToString(payloadBytes) != payload || base64.RawURLEncoding.EncodeToString(macBytes) != mac || timeErr != nil {
		return "", errors.New("Hub returned an invalid enrollment challenge")
	}
	selector := enrollmentPublicSelector(key)
	signature := ed25519.Sign(key, enrollmentSigningMessage(c.HubOrigin, c.Organization, selector, reply.Challenge, body))
	header := "RadarSignature org=" + c.Organization + ",key=" + selector + ",challenge=" + reply.Challenge + ",signature=" + base64.RawURLEncoding.EncodeToString(signature)
	if len(header) > maxProofHeaderBytes {
		return "", errors.New("Hub returned an enrollment challenge too large to sign")
	}
	return header, nil
}
