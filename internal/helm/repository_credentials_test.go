package helm

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/repo"
)

func TestRepositoryRedirectCredentials(t *testing.T) {
	for _, allow := range []bool{false, true} {
		for _, consumer := range []string{"source", "refresh", "add", "install", "progress", "detail", "prepared"} {
			t.Run(fmt.Sprintf("%s/passAll=%t", consumer, allow), func(t *testing.T) {
				c := newChartSourceTestClient(t)
				path, err := chartutil.Save(sourceTestChart("publisher"), t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				archive, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var seen atomic.Bool
				index := fmt.Sprintf("apiVersion: v1\nentries:\n  radar:\n  - apiVersion: v2\n    name: radar\n    version: 1.0.0\n    urls: [chart.tgz]\n    digest: %x\n", sha256.Sum256(archive))
				target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					seen.Store(r.Header.Get("Authorization") != "")
					if consumer != "refresh" && consumer != "add" {
						w.Write(archive)
					} else {
						fmt.Fprint(w, index)
					}
				}))
				defer target.Close()
				var origin *httptest.Server
				origin = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Authorization") == "" {
						t.Error("same-origin request lost credentials")
					}
					if r.URL.Path == "/index.yaml" && consumer != "refresh" && consumer != "add" {
						fmt.Fprint(w, index)
					} else {
						http.Redirect(w, r, target.URL+"/target", http.StatusFound)
					}
				}))
				defer origin.Close()
				f := repo.NewFile()
				f.Update(&repo.Entry{Name: "probe", URL: origin.URL, Username: "sample", Password: "sample", PassCredentialsAll: allow})
				if err := f.WriteFile(c.settings.RepositoryConfig, 0600); err != nil {
					t.Fatal(err)
				}
				switch consumer {
				case "source":
					_, _, err = c.loadSourceChart(nil, ChartSourceCandidate{Type: "repository", Reference: "probe", URL: origin.URL}, "radar", "1.0.0")
				case "refresh":
					err = c.UpdateRepository("probe")
				case "add":
					_, err = c.ensureClassicRepository(origin.URL, "probe")
				default:
					if err = c.UpdateRepository("probe"); err != nil {
						t.Fatal(err)
					}
					cfg := memoryActionConfig(t)
					req := &InstallRequest{ReleaseName: "credentials", Namespace: "default", Repository: "probe", ChartName: "radar", Version: "1.0.0"}
					switch consumer {
					case "install":
						_, err = c.installWith(cfg, req)
					case "progress":
						_, err = c.installWithProgressUsing(cfg, req, make(chan InstallProgress, 100))
					case "detail":
						_, err = c.GetChartDetail("probe", "radar", "1.0.0")
					case "prepared":
						_, _, err = c.resolvePreparedChart(context.Background(), req, "0.1.0")
					}

				}
				if err != nil {
					t.Fatal(err)
				}
				if seen.Load() != allow {
					t.Fatalf("cross-port destination received credentials=%t; explicit opt-in=%t", seen.Load(), allow)
				}
			})
		}
	}
}

func TestRepositoryClientCertificateIsolation(t *testing.T) {
	for _, allow := range []bool{false, true} {
		for _, redirect := range []bool{false, true} {
			t.Run(fmt.Sprintf("passAll=%t/redirect=%t", allow, redirect), func(t *testing.T) {
				var targetCertificate, originCertificate atomic.Bool
				target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					targetCertificate.Store(len(r.TLS.PeerCertificates) > 0)
					fmt.Fprint(w, "archive")
				}))
				target.TLS = &tls.Config{ClientAuth: tls.RequestClientCert}
				target.StartTLS()
				defer target.Close()
				origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					originCertificate.Store(len(r.TLS.PeerCertificates) > 0)
					if redirect {
						http.Redirect(w, r, target.URL, http.StatusFound)
					} else {
						fmt.Fprint(w, "index")
					}
				}))
				origin.TLS = &tls.Config{ClientAuth: tls.RequestClientCert}
				origin.StartTLS()
				defer origin.Close()
				cert := origin.TLS.Certificates[0]
				dir := t.TempDir()
				certPath := filepath.Join(dir, "client.pem")
				keyPath := filepath.Join(dir, "client-key.pem")
				caPath := filepath.Join(dir, "ca.pem")
				key, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
				if err != nil {
					t.Fatal(err)
				}
				for name, data := range map[string][]byte{certPath: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), keyPath: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), caPath: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: origin.Certificate().Raw})} {
					if err := os.WriteFile(name, data, 0600); err != nil {
						t.Fatal(err)
					}
				}
				g := repositoryHTTPGetter{entry: repo.Entry{URL: origin.URL, CAFile: caPath, CertFile: certPath, KeyFile: keyPath, PassCredentialsAll: allow}}
				if _, err := g.Get(origin.URL); err != nil {
					t.Fatal(err)
				}
				if !originCertificate.Load() {
					t.Fatal("repository did not receive configured client certificate")
				}
				if !redirect {
					if _, err := g.Get(target.URL); err != nil {
						t.Fatal(err)
					}
				}
				if targetCertificate.Load() != allow {
					t.Fatalf("cross-origin client certificate received=%t opt-in=%t", targetCertificate.Load(), allow)
				}
			})
		}
	}
}

func TestRepositoryOriginAndRedirectPolicy(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"https://EXAMPLE.com/a", "https://example.com:443/b", true},
		{"http://example.com/a", "http://example.com:80/b", true},
		{"https://example.com", "http://example.com", false},
		{"http://example.com:80", "http://example.com:81", false},
		{"https://example.com", "https://sub.example.com", false},
	} {
		a, _ := url.Parse(tc.a)
		b, _ := url.Parse(tc.b)
		if got := sameHTTPOrigin(a, b); got != tc.want {
			t.Errorf("%s -> %s = %t", tc.a, tc.b, got)
		}
	}
	for _, destination := range []string{"same", "hostname", "downgrade"} {
		t.Run(destination, func(t *testing.T) {
			var received atomic.Bool
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received.Store(r.Header.Get("Authorization") != "")
				fmt.Fprint(w, "archive")
			}))
			defer target.Close()
			var origin *httptest.Server
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/done" {
					received.Store(r.Header.Get("Authorization") != "")
					fmt.Fprint(w, "archive")
					return
				}
				to := target.URL
				if destination == "same" {
					to = origin.URL + "/done"
				}
				if destination == "hostname" {
					to = strings.Replace(to, "127.0.0.1", "localhost", 1)
				}
				http.Redirect(w, r, to, http.StatusFound)
			})
			if destination == "downgrade" {
				origin = httptest.NewTLSServer(handler)
			} else {
				origin = httptest.NewServer(handler)
			}
			defer origin.Close()
			g := repositoryHTTPGetter{entry: repo.Entry{URL: origin.URL, Username: "sample", Password: "sample", InsecureSkipTLSverify: true}}
			if _, err := g.Get(origin.URL); err != nil {
				t.Fatal(err)
			}
			if received.Load() != (destination == "same") {
				t.Fatalf("unexpected credential forwarding to %s", destination)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (repositoryHTTPGetter{entry: repo.Entry{URL: "http://127.0.0.1"}}).getContext(ctx, "http://127.0.0.1"); err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestRepositoryCAWithInsecureTLS(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.UserAgent(), "Helm/") {
			t.Error("missing Helm user agent")
		}
		fmt.Fprint(w, "archive")
	}))
	defer server.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	g := repositoryHTTPGetter{entry: repo.Entry{URL: server.URL, CAFile: ca, InsecureSkipTLSverify: true}}
	if _, err := g.Get(server.URL); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryChartKeepsPluginGetter(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-based Helm downloader plugin fixture")
	}
	c := newChartSourceTestClient(t)
	repoConfigPath, cache := c.settings.RepositoryConfig, c.settings.RepositoryCache
	c.settings = cli.New()
	c.settings.RepositoryConfig = repoConfigPath
	c.settings.RepositoryCache = cache
	plugins := t.TempDir()
	c.settings.PluginsDirectory = plugins
	dir := filepath.Join(plugins, "fixture")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	config := "name: fixture\nversion: 0.1.0\nusage: fixture\ndescription: local test downloader\ndownloaders:\n- command: download.sh\n  protocols: [fixture, https]\n"
	if err := os.WriteFile(filepath.Join(dir, "plugin.yaml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "download.sh"), []byte("#!/bin/sh\nprintf 'plugin archive'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	for key := range c.settings.EnvVars() {
		t.Setenv(key, os.Getenv(key))
	}
	t.Setenv("HELM_PLUGIN_NAME", os.Getenv("HELM_PLUGIN_NAME"))
	t.Setenv("HELM_PLUGIN_DIR", os.Getenv("HELM_PLUGIN_DIR"))
	got, err := c.getRepositoryChart(&repo.Entry{URL: "fixture://repo"}, "fixture://repo/chart.tgz")
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != "plugin archive" {
		t.Fatalf("plugin result %q", got.String())
	}
}

func TestRepositoryIndexOCIArchiveError(t *testing.T) {
	c := newChartSourceTestClient(t)
	path := filepath.Join(t.TempDir(), "bad.tgz")
	if err := os.WriteFile(path, []byte("not a chart archive"), 0600); err != nil {
		t.Fatal(err)
	}
	c.chartPathLocator = func(*action.Configuration, string, string) (string, error) { return path, nil }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "apiVersion: v1\nentries:\n  radar:\n  - apiVersion: v2\n    name: radar\n    version: 1.0.0\n    urls: [oci://registry.example.test/charts/radar]\n")
	}))
	defer server.Close()
	f := repo.NewFile()
	f.Update(&repo.Entry{Name: "probe", URL: server.URL})
	if err := f.WriteFile(c.settings.RepositoryConfig, 0600); err != nil {
		t.Fatal(err)
	}
	if err := c.UpdateRepository("probe"); err != nil {
		t.Fatal(err)
	}
	req := &InstallRequest{ReleaseName: "proof", Namespace: "default", Repository: "probe", ChartName: "radar", Version: "1.0.0"}
	if _, err := c.installWith(memoryActionConfig(t), req); err == nil {
		t.Fatal("invalid OCI archive accepted")
	}
	detail, err := c.GetChartDetail("probe", "radar", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Name != "radar" || detail.Version != "1.0.0" {
		t.Fatalf("basic index detail changed: %+v", detail)
	}
}

func TestRepositoryForeignHostTLS(t *testing.T) {
	// A distinct certificate represents a public chart host trusted by system roots.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(100), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	foreign := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "archive") }))
	foreign.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	foreign.StartTLS()
	defer foreign.Close()
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer origin.Close()
	ca := filepath.Join(t.TempDir(), "private-ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: origin.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	publicRoots := x509.NewCertPool()
	publicRoots.AddCert(foreign.Certificate())
	for _, skip := range []bool{false, true} {
		auth, anonymous, err := repositoryTLSConfigs(repo.Entry{CAFile: ca, InsecureSkipTLSverify: skip}, publicRoots)
		if err != nil {
			t.Fatal(err)
		}
		if auth.InsecureSkipVerify != skip || anonymous.InsecureSkipVerify {
			t.Fatal("TLS verification escaped the repository origin")
		}
		tr := &http.Transport{TLSClientConfig: anonymous}
		defer tr.CloseIdleConnections()
		resp, err := (&http.Client{Transport: tr}).Get(foreign.URL)
		if err != nil {
			t.Fatalf("system-trusted foreign archive: %v", err)
		}
		resp.Body.Close()
	}
	_, untrusted, err := repositoryTLSConfigs(repo.Entry{CAFile: ca, InsecureSkipTLSverify: true}, x509.NewCertPool())
	if err != nil {
		t.Fatal(err)
	}
	tr := &http.Transport{TLSClientConfig: untrusted}
	defer tr.CloseIdleConnections()
	if resp, err := (&http.Client{Transport: tr}).Get(foreign.URL); err == nil {
		resp.Body.Close()
		t.Fatal("foreign host bypassed TLS verification")
	}
}

func TestRepositoryPreparedFetchLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, strings.Repeat("a", 4096)) }))
	defer server.Close()
	entry := repo.Entry{URL: server.URL}
	if _, err := (repositoryHTTPGetter{entry: entry, maxBytes: 1024}).Get(server.URL); err == nil || !strings.Contains(err.Error(), "1024 bytes") {
		t.Fatalf("bounded prepared transport: %v", err)
	}
	body, err := (repositoryHTTPGetter{entry: entry}).Get(server.URL)
	if err != nil || body.Len() != 4096 {
		t.Fatalf("legacy download compatibility: %v", err)
	}
}
