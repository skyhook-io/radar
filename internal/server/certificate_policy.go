package server

import (
	"github.com/skyhook-io/radar/internal/audit"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/pkg/health"
	"github.com/skyhook-io/radar/pkg/topology"
	"net/http"
	"time"
)

func certificatePolicyHandler(h http.Handler, policy health.CertificateExpiryPolicy) http.Handler {
	if h == nil {
		return nil
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(audit.WithCertificateExpiryPolicy(r.Context(), policy)))
	})
}

func (s *Server) newIssueProvider() *issues.CacheProvider {
	return issues.NewCacheProvider(s.certificateExpiryPolicy)
}

func (s *Server) applyCertificateExpiryPolicy(info *topology.SecretCertificateInfo) {
	if info == nil {
		return
	}
	windowsFor := func(cert topology.CertificateInfo) health.CertificateExpiryWindows {
		var start, end *time.Time
		if at, err := time.Parse(time.RFC3339, cert.NotBefore); err == nil {
			start = &at
		}
		if at, err := time.Parse(time.RFC3339, cert.NotAfter); err == nil {
			end = &at
		}
		return s.certificateExpiryPolicy.Resolve(start, end).Windows()
	}
	for i := range info.Certificates {
		windows := windowsFor(info.Certificates[i])
		info.Certificates[i].ExpiryPolicy = &windows
	}
	for key := range info.AdditionalKeys {
		for i := range info.AdditionalKeys[key] {
			windows := windowsFor(info.AdditionalKeys[key][i])
			info.AdditionalKeys[key][i].ExpiryPolicy = &windows
		}
	}
}
