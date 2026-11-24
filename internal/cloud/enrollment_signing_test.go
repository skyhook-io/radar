package cloud

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func signingTestKey(t *testing.T, seed byte) (ed25519.PrivateKey, string) {
	t.Helper()
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize))
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "private.pem")
	if err := os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return key, file
}

func testChallenge() string {
	return base64.RawURLEncoding.EncodeToString([]byte(`{"version":1,"id":"test","issued_at":1,"expires_at":61}`)) + "." + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32))
}

func TestSignedEnrollmentPreservesAuthenticatedKeyRefusal(t *testing.T) {
	_, file := signingTestKey(t, 54)
	for _, code := range []string{"key_deleted", "key_replaced"} {
		t.Run(code, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == enrollmentChallengePath {
					_ = json.NewEncoder(w).Encode(map[string]string{"challenge": testChallenge(), "expires_at": time.Now().Add(time.Minute).UTC().Format(time.RFC3339)})
					return
				}
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "message": "Put a current key in this cluster's enrollment Secret."})
			}))
			defer srv.Close()
			client, err := NewSignedEnrollClient(srv.URL, file, "org_customer", nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Enroll(context.Background(), EnrollRequest{AgentUID: testClusterUID, Namespace: "radar", Release: "radar", Name: "test"})
			var refusal *EnrollError
			if !errors.As(err, &refusal) || refusal.Code != code || !strings.Contains(refusal.Message, "current key") {
				t.Fatalf("lost actionable refusal: %v", err)
			}
		})
	}
}

func TestSignedEnrollmentHTTPBindsIdentityAndBody(t *testing.T) {
	key, file := signingTestKey(t, 42)
	challenge := testChallenge()
	var origin string
	var enrolls, challenges int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case enrollmentChallengePath:
			challenges++
			if r.Method != http.MethodPost || r.Header.Get("Authorization") != "" {
				t.Error("challenge should be an unauthenticated POST")
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"challenge": challenge, "expires_at": time.Now().Add(time.Minute).UTC().Format(time.RFC3339)})
		case enrollPath:
			enrolls++
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			fields := strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "RadarSignature "), ",")
			if len(fields) != 4 || fields[0] != "org=org_customer" || fields[1] != "key="+enrollmentPublicSelector(key) || fields[2] != "challenge="+challenge {
				t.Fatalf("unexpected proof fields: %v", fields)
			}
			sig, err := base64.RawURLEncoding.Strict().DecodeString(strings.TrimPrefix(fields[3], "signature="))
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(body)
			message := []byte("radar-hub-enrollment-v1\n" + origin + "\norg_customer\n" + enrollmentPublicSelector(key) + "\n" + challenge + "\n" + base64.RawURLEncoding.EncodeToString(digest[:]))
			public := ed25519.PublicKey(key[ed25519.SeedSize:])
			if !ed25519.Verify(public, message, sig) {
				t.Fatal("signature failed independent verification over received exact body")
			}
			for _, altered := range [][]byte{bytes.ReplaceAll(message, []byte("org_customer"), []byte("org_attacker")), append(append([]byte(nil), message...), '\n')} {
				if ed25519.Verify(public, altered, sig) {
					t.Fatal("signature accepted a changed org or message")
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"cluster_id": "cluster", "token": "rhc_live", "agent_url": "wss://hub.example.com/agent", "org_id": "org_customer", "org_name": "Customer"})
		default:
			t.Errorf("unexpected URL %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	origin = srv.URL
	client, err := NewSignedEnrollClient(origin, file, "org_customer", nil)
	if err != nil {
		t.Fatal(err)
	}
	request := EnrollRequest{AgentUID: testClusterUID, Namespace: "radar", Release: "radar", Name: "prod"}
	for attempt := range 2 {
		result, err := client.Enroll(context.Background(), request)
		if err != nil || result.OrganizationID != "org_customer" {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		if attempt == 0 {
			var nextFile string
			key, nextFile = signingTestKey(t, 43)
			nextPEM, err := os.ReadFile(nextFile)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, nextPEM, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if challenges != 2 || enrolls != 2 {
		t.Fatalf("each attempt needs a new challenge: challenges=%d enrolls=%d", challenges, enrolls)
	}
}

func TestSignedEnrollmentRequiresTrustedTLS(t *testing.T) {
	_, file := signingTestKey(t, 42)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == enrollmentChallengePath {
			_ = json.NewEncoder(w).Encode(map[string]string{"challenge": testChallenge(), "expires_at": time.Now().UTC().Format(time.RFC3339)})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"cluster_id": "cluster", "token": "rhc_live", "agent_url": "wss://hub.example.com/agent", "org_id": "org_customer"})
	}))
	defer srv.Close()
	client, err := NewSignedEnrollClient(srv.URL, file, "org_customer", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Enroll(context.Background(), EnrollRequest{Name: "prod"}); err == nil {
		t.Fatal("self-signed TLS accepted without a trusted CA")
	}
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	client, err = NewSignedEnrollClient(srv.URL, file, "org_customer", roots)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Enroll(context.Background(), EnrollRequest{Name: "prod"}); err != nil {
		t.Fatalf("private CA did not enable verified TLS: %v", err)
	}
}

func TestSignedEnrollmentRejectsRedirectsAndWrongOrganization(t *testing.T) {
	_, file := signingTestKey(t, 42)
	for _, stage := range []string{"challenge_redirect", "enroll_redirect", "wrong_org", "no_org", "unsupported", "oversized_challenge", "bad_challenge"} {
		t.Run(stage, func(t *testing.T) {
			forwarded := 0
			destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded++ }))
			defer destination.Close()
			enrolls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == enrollmentChallengePath {
					switch stage {
					case "challenge_redirect":
						http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
					case "unsupported":
						w.WriteHeader(http.StatusNotFound)
					case "oversized_challenge":
						_, _ = w.Write([]byte(strings.Repeat("x", maxChallengeBytes+1)))
					case "bad_challenge":
						_, _ = w.Write([]byte(`{"challenge":"not-a-token"}`))
					default:
						_ = json.NewEncoder(w).Encode(map[string]string{"challenge": testChallenge(), "expires_at": time.Now().UTC().Format(time.RFC3339)})
					}
					return
				}
				enrolls++
				if stage == "enroll_redirect" {
					http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
					return
				}
				org := "org_attacker"
				if stage == "no_org" {
					org = ""
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"cluster_id": "cluster", "token": "rhc_live", "agent_url": "wss://hub.example.com/agent", "org_id": org})
			}))
			defer srv.Close()
			client, err := NewSignedEnrollClient(srv.URL, file, "org_customer", nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Enroll(context.Background(), EnrollRequest{Name: "prod"}); err == nil {
				t.Fatal("unsafe response accepted")
			} else if stage == "unsupported" {
				var response *EnrollError
				if !errors.As(err, &response) || response.Code != "public_key_enrollment_unavailable" || response.RetryAfter != 0 {
					t.Fatalf("unsupported Hub should use normal retry: %v", err)
				}
			}
			if forwarded != 0 {
				t.Fatal("proof followed a redirect")
			}
			if (stage == "unsupported" || strings.Contains(stage, "challenge")) && enrolls != 0 {
				t.Fatal("failed challenge fell through to enrollment")
			}
		})
	}
}

func TestEnrollmentPrivateKeyParsingAndReload(t *testing.T) {
	key, file := signingTestKey(t, 42)
	valid, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	validBlock, _ := pem.Decode(valid)
	trailingDER := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: append(validBlock.Bytes, 0)})
	malformedFirstBlock := append([]byte("-----BEGIN PRIVATE KEY-----\nnot valid base64\n"), valid...)
	for _, input := range [][]byte{nil, []byte("not PEM"), append(append([]byte(nil), valid...), valid...), append(append([]byte(nil), valid...), []byte("trailing")...), trailingDER, malformedFirstBlock, bytes.ReplaceAll(valid, []byte("PRIVATE KEY"), []byte("ENCRYPTED PRIVATE KEY")), bytes.Repeat([]byte{'x'}, maxEnrollmentKeyBytes+1)} {
		if err := os.WriteFile(file, input, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadEnrollmentPrivateKey(file); err == nil {
			t.Fatal("invalid private-key file accepted")
		} else if strings.Contains(err.Error(), string(valid)) {
			t.Fatal("private key exposed in error")
		}
	}
	if err := os.WriteFile(file, valid, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadEnrollmentPrivateKey(file)
	if err != nil || !bytes.Equal(got, key) {
		t.Fatalf("valid key rejected: %v", err)
	}
	other, otherFile := signingTestKey(t, 43)
	newBytes, err := os.ReadFile(otherFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, newBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = ReadEnrollmentPrivateKey(file)
	if err != nil || !bytes.Equal(got, other) {
		t.Fatalf("projected key update not reread: %v", err)
	}
}

func TestSignedEnrollmentOrigin(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"https://Hub.Example.com:443/", "https://hub.example.com"},
		{"http://LOCALHOST:80/", "http://localhost"},
		{"https://[::1]:443/", "https://[::1]"},
		{"https://xn--bcher-kva.example/", "https://xn--bcher-kva.example"},
	} {
		got, err := NormalizeEnrollmentOrigin(tc.raw)
		if err != nil || got != tc.want {
			t.Errorf("origin %q = %q, %v; want %q", tc.raw, got, err, tc.want)
		}
	}
	for _, raw := range []string{"https://bücher.example", "https://hub.example.com/path", "http://hub.example.com", "https://hub.example.com?query", "https://hub.example.com\n"} {
		if _, err := NormalizeEnrollmentOrigin(raw); err == nil {
			t.Errorf("invalid origin %q accepted", raw)
		}
	}
}

func TestEnrollmentSigningGoldenVector(t *testing.T) {
	raw, err := os.ReadFile("testdata/enrollment-signature-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		Seed, PublicKey, Origin, Organization, Selector, Challenge, Body, Message, Signature string
	}
	if err := json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	seed, err := hex.DecodeString(vector.Seed)
	if err != nil {
		t.Fatal(err)
	}
	key := ed25519.NewKeyFromSeed(seed)
	message := enrollmentSigningMessage(vector.Origin, vector.Organization, enrollmentPublicSelector(key), vector.Challenge, []byte(vector.Body))
	if string(message) != vector.Message || enrollmentPublicSelector(key) != vector.Selector || base64.RawURLEncoding.EncodeToString(key[32:]) != vector.PublicKey || base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, message)) != vector.Signature {
		t.Fatal("wire signing profile differs from independent fixed vector")
	}
}
