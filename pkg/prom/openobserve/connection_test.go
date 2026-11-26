package openobserve

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func validConn() Connection {
	return Connection{BaseURL: "https://oo.example.com:5080/oo", Org: "default", User: "admin@example.com", Token: "t0k"}
}

func TestConnectionValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Connection)
		ok     bool
	}{
		{"valid", func(*Connection) {}, true},
		{"http and trailing slash", func(c *Connection) { c.BaseURL = "http://localhost:5080/" }, true},
		{"empty base", func(c *Connection) { c.BaseURL = "" }, false},
		{"no scheme", func(c *Connection) { c.BaseURL = "oo.example.com" }, false},
		{"ftp", func(c *Connection) { c.BaseURL = "ftp://oo.example.com" }, false},
		{"no host", func(c *Connection) { c.BaseURL = "https:///x" }, false},
		{"userinfo", func(c *Connection) { c.BaseURL = "https://a:b@oo.example.com" }, false},
		{"query", func(c *Connection) { c.BaseURL = "https://oo.example.com?x=1" }, false},
		{"empty query", func(c *Connection) { c.BaseURL = "https://oo.example.com?" }, false},
		{"fragment", func(c *Connection) { c.BaseURL = "https://oo.example.com#x" }, false},
		{"empty org", func(c *Connection) { c.Org = "" }, false},
		{"org traversal", func(c *Connection) { c.Org = ".." }, false},
		{"org slash", func(c *Connection) { c.Org = "a/b" }, false},
		{"org space", func(c *Connection) { c.Org = "a b" }, false},
		{"org with dash and dot", func(c *Connection) { c.Org = "team-a.prod_1" }, true},
		{"empty user", func(c *Connection) { c.User = "" }, false},
		{"user colon", func(c *Connection) { c.User = "a:b" }, false},
		{"empty token", func(c *Connection) { c.Token = "" }, false},
		{"token newline", func(c *Connection) { c.Token = "a\r\nX-Evil: 1" }, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := validConn()
			tc.mutate(&c)
			err := c.Validate()
			if tc.ok && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestConnectionURLAndAuth(t *testing.T) {
	c := validConn()
	if got := c.PrometheusURL(); got != "https://oo.example.com:5080/oo/api/default/prometheus" {
		t.Fatalf("PrometheusURL = %s", got)
	}
	c.BaseURL = "https://oo.example.com/"
	if got := c.PrometheusURL(); got != "https://oo.example.com/api/default/prometheus" {
		t.Fatalf("PrometheusURL = %s", got)
	}
	c = Connection{User: "root@example.com", Token: "Complexpass#123"}
	if got := c.AuthHeader(); got != "Basic cm9vdEBleGFtcGxlLmNvbTpDb21wbGV4cGFzcyMxMjM=" {
		t.Fatalf("AuthHeader = %s", got)
	}
}

func TestConnectionTLSConfig(t *testing.T) {
	caPEM, serverCert := newTestCA(t)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{serverCert}}
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	t.Cleanup(srv.Close)

	get := func(c Connection) error {
		cfg, err := c.TLSConfig()
		if err != nil {
			return err
		}
		client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: cfg}}
		resp, err := client.Get(srv.URL)
		if err != nil {
			return err
		}
		_ = resp.Body.Close()
		return nil
	}

	if err := get(Connection{CACertPEM: caPEM}); err != nil {
		t.Fatalf("with the CA: %v", err)
	}
	if err := get(Connection{}); err == nil {
		t.Fatal("without the CA the self-signed server was trusted")
	}
	cfg, err := Connection{}.TLSConfig()
	if err != nil || cfg.MinVersion < tls.VersionTLS12 || cfg.InsecureSkipVerify {
		t.Fatalf("default TLS config = %+v, %v", cfg, err)
	}
	if _, err := (Connection{CACertPEM: "-----BEGIN CERTIFICATE-----\nnope\n-----END CERTIFICATE-----\n"}).TLSConfig(); err == nil {
		t.Fatal("bad PEM accepted")
	}
	if _, err := (Connection{CACertPEM: "not pem at all"}).TLSConfig(); err == nil {
		t.Fatal("non-PEM accepted")
	}
}

func TestConnectionNewHTTPTransport(t *testing.T) {
	c := validConn()
	tr, err := c.NewHTTPTransport(0)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Address() != c.PrometheusURL() || tr.Headers["Authorization"] != c.AuthHeader() {
		t.Fatalf("transport = %s %v", tr.Address(), tr.Headers)
	}
	c.Org = ""
	if _, err := c.NewHTTPTransport(0); err == nil {
		t.Fatal("invalid connection accepted")
	}
}

func newTestCA(t *testing.T) (string, tls.Certificate) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}))
	return caPEM, tls.Certificate{Certificate: [][]byte{leafDER}, PrivateKey: leafKey}
}
