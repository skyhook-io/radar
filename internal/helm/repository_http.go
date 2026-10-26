package helm

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/getter"
	"helm.sh/helm/v3/pkg/repo"
)

func sameHTTPOrigin(a, b *url.URL) bool {
	port := func(u *url.URL) string {
		if p := u.Port(); p != "" {
			return p
		}
		if strings.EqualFold(u.Scheme, "https") {
			return "443"
		}
		return "80"
	}
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Hostname(), b.Hostname()) && port(a) == port(b)
}

type repositoryHTTPGetter struct {
	entry    repo.Entry
	timeout  time.Duration
	maxBytes int64
}

// A repository snapshot owns its credentials. Helm's default HTTP client
// follows redirects using hostname-only authorization forwarding.
func (g repositoryHTTPGetter) Get(target string, _ ...getter.Option) (*bytes.Buffer, error) {
	return g.getContext(context.Background(), target)
}

func (g repositoryHTTPGetter) getContext(ctx context.Context, target string) (*bytes.Buffer, error) {
	origin, err := url.Parse(g.entry.URL)
	if err != nil {
		return nil, fmt.Errorf("invalid repository URL")
	}
	publicRoots, _ := x509.SystemCertPool()
	tlsConfig, anonymousTLS, err := repositoryTLSConfigs(g.entry, publicRoots)
	if err != nil {
		return nil, err
	}
	authenticated := &http.Transport{Proxy: http.ProxyFromEnvironment, DisableCompression: true, TLSClientConfig: tlsConfig, DialContext: (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext, TLSHandshakeTimeout: 10 * time.Second}
	defer authenticated.CloseIdleConnections()
	anonymous := authenticated.Clone()
	anonymous.TLSClientConfig = anonymousTLS
	defer anonymous.CloseIdleConnections()
	timeout := g.timeout
	if timeout == 0 {
		timeout = time.Second * getter.DefaultHTTPTimeout
	}
	client := &http.Client{Timeout: timeout, Transport: repositoryOriginTransport{origin: origin, allow: g.entry.PassCredentialsAll, authenticated: authenticated, anonymous: anonymous}, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		if !g.entry.PassCredentialsAll && !sameHTTPOrigin(origin, req.URL) {
			req.Header.Del("Authorization")
			req.Header.Del("Cookie")
		}
		return nil
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid chart download URL")
	}
	if (g.entry.PassCredentialsAll || sameHTTPOrigin(origin, req.URL)) && g.entry.Username != "" && g.entry.Password != "" {
		req.SetBasicAuth(g.entry.Username, g.entry.Password)
	}
	req.Header.Set("User-Agent", "Helm/"+strings.TrimPrefix(chartutil.DefaultCapabilities.HelmVersion.Version, "v"))
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("repository request failed: %s", repositoryRefreshErrorURL.ReplaceAllString(err.Error(), "<redacted>"))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("repository returned HTTP %d", resp.StatusCode)
	}
	var body io.Reader = resp.Body
	if g.maxBytes > 0 {
		body = io.LimitReader(resp.Body, g.maxBytes+1)
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}
	if g.maxBytes > 0 && int64(len(data)) > g.maxBytes {
		return nil, fmt.Errorf("repository response exceeds %d bytes", g.maxBytes)
	}
	return bytes.NewBuffer(data), nil
}

type repositoryOriginTransport struct {
	origin                   *url.URL
	allow                    bool
	authenticated, anonymous *http.Transport
}

func (t repositoryOriginTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.allow || sameHTTPOrigin(t.origin, req.URL) {
		return t.authenticated.RoundTrip(req)
	}
	clean := req.Clone(req.Context())
	clean.Header.Del("Authorization")
	clean.Header.Del("Cookie")
	return t.anonymous.RoundTrip(clean)
}

func (c *Client) repositoryGettersWithTimeout(entry *repo.Entry, timeout time.Duration) getter.Providers {
	providers := getter.All(c.settings)
	for i := range providers {
		for _, scheme := range providers[i].Schemes {
			if scheme == "http" || scheme == "https" {
				snapshot := *entry
				providers[i].New = func(_ ...getter.Option) (getter.Getter, error) {
					return repositoryHTTPGetter{entry: snapshot, timeout: timeout}, nil
				}
				// Helm lists its built-in HTTP provider before downloader plugins.
				return providers
			}
		}
	}
	return providers
}

func (c *Client) repositoryEntryForRequest(repository string) (*repo.Entry, error) {
	if strings.HasPrefix(repository, "http://") || strings.HasPrefix(repository, "https://") {
		return &repo.Entry{URL: repository}, nil
	}
	f, err := repo.LoadFile(c.settings.RepositoryConfig)
	if err != nil {
		return nil, err
	}
	for _, entry := range f.Repositories {
		if entry.Name == repository {
			snapshot := *entry
			return &snapshot, nil
		}
	}
	return nil, fmt.Errorf("repository %s not found", repository)
}

func (c *Client) getRepositoryChart(entry *repo.Entry, target string) (*bytes.Buffer, error) {
	parsed, err := url.Parse(target)
	if err != nil {
		return nil, fmt.Errorf("invalid chart URL")
	}
	provider, err := c.repositoryGetters(entry).ByScheme(parsed.Scheme)
	if err != nil {
		return nil, err
	}
	return provider.Get(target, getter.WithURL(entry.URL), getter.WithBasicAuth(entry.Username, entry.Password), getter.WithTLSClientConfig(entry.CertFile, entry.KeyFile, entry.CAFile), getter.WithInsecureSkipVerifyTLS(entry.InsecureSkipTLSverify), getter.WithPassCredentialsAll(entry.PassCredentialsAll))
}

func (c *Client) repositoryGetters(entry *repo.Entry) getter.Providers {
	return c.repositoryGettersWithTimeout(entry, time.Second*getter.DefaultHTTPTimeout)
}

func repositoryTLSConfigs(entry repo.Entry, publicRoots *x509.CertPool) (*tls.Config, *tls.Config, error) {
	tlsConfig := &tls.Config{InsecureSkipVerify: entry.InsecureSkipTLSverify}
	var caData []byte
	if entry.CertFile != "" && entry.KeyFile != "" {
		cert, err := tls.LoadX509KeyPair(entry.CertFile, entry.KeyFile)
		if err != nil {
			return nil, nil, fmt.Errorf("configure repository client certificate: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}
	if entry.CAFile != "" {
		data, err := os.ReadFile(entry.CAFile)
		if err != nil {
			return nil, nil, fmt.Errorf("read repository CA: %w", err)
		}
		caData = data
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(data) {
			return nil, nil, fmt.Errorf("repository CA file contains no certificates")
		}
		tlsConfig.RootCAs = pool
	}

	anonymous := tlsConfig.Clone()
	anonymous.Certificates = nil
	anonymous.GetClientCertificate = nil
	anonymous.InsecureSkipVerify = false
	if publicRoots == nil {
		publicRoots = x509.NewCertPool()
	}
	anonymous.RootCAs = publicRoots.Clone()
	if len(caData) > 0 {
		anonymous.RootCAs.AppendCertsFromPEM(caData)
	}

	return tlsConfig, anonymous, nil
}
