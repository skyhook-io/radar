package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

const ServerName = "api.ate-system.svc"

// State is the on-disk material Radar is configured with. It is created once
// and reused, so the CA path and token files stay valid across restarts.
type State struct {
	Dir          string
	CAFile       string
	CertFile     string
	KeyFile      string
	TokenFile    string
	LimitedFile  string
	FaultFile    string
	GlobalToken  string
	LimitedToken string
}

func LoadOrCreateState(dir string) (*State, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	st := &State{
		Dir:         dir,
		CAFile:      filepath.Join(dir, "ca.pem"),
		CertFile:    filepath.Join(dir, "server.pem"),
		KeyFile:     filepath.Join(dir, "server-key.pem"),
		TokenFile:   filepath.Join(dir, "token"),
		LimitedFile: filepath.Join(dir, "token-limited"),
		FaultFile:   filepath.Join(dir, "fault"),
	}
	if err := ensureCerts(st); err != nil {
		return nil, err
	}
	var err error
	if st.GlobalToken, err = ensureToken(st.TokenFile); err != nil {
		return nil, err
	}
	if st.LimitedToken, err = ensureToken(st.LimitedFile); err != nil {
		return nil, err
	}
	return st, nil
}

func ensureToken(path string) (string, error) {
	if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
		return string(trimNL(b)), nil
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	tok := "fake-ate-" + hex.EncodeToString(buf)
	return tok, os.WriteFile(path, []byte(tok+"\n"), 0o600)
}

func trimNL(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r' || b[len(b)-1] == ' ') {
		b = b[:len(b)-1]
	}
	return b
}

func ensureCerts(st *State) error {
	if _, err := tls.LoadX509KeyPair(st.CertFile, st.KeyFile); err == nil {
		if _, err := os.Stat(st.CAFile); err == nil {
			return nil
		}
	}
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	now := time.Now()
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "substrate-demo fake CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(5 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return err
	}
	caCert, _ := x509.ParseCertificate(caDER)
	srvKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	srvTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: ServerName},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(2 * 365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{ServerName, ServerName + ".cluster.local", "localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	srvDER, err := x509.CreateCertificate(rand.Reader, srvTmpl, caCert, &srvKey.PublicKey, caKey)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalECPrivateKey(srvKey)
	if err != nil {
		return err
	}
	return errors.Join(
		writePEM(st.CAFile, "CERTIFICATE", caDER, 0o644),
		writePEM(st.CertFile, "CERTIFICATE", srvDER, 0o644),
		writePEM(st.KeyFile, "EC PRIVATE KEY", keyDER, 0o600),
	)
}

func writePEM(path, typ string, der []byte, mode os.FileMode) error {
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), mode); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func ServerTLS(st *State) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(st.CertFile, st.KeyFile)
	if err != nil {
		return nil, err
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}, nil
}

func ClientTLS(caFile, serverName string) (*tls.Config, error) {
	pemBytes, err := os.ReadFile(caFile)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, fmt.Errorf("no certificates in %s", caFile)
	}
	return &tls.Config{RootCAs: pool, ServerName: serverName, MinVersion: tls.VersionTLS13}, nil
}
