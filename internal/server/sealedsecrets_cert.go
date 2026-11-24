package server

import (
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/rest"
)

// sealedSecretsCertPath is the controller's public-certificate route, the one
// `kubeseal --fetch-cert` reads. It is the only path this endpoint requests.
const sealedSecretsCertPath = "/v1/cert.pem"

const (
	sealedSecretsCertTimeout  = 5 * time.Second
	sealedSecretsCertMaxBytes = 64 << 10
)

// sealedSecretsCertDialContext dials the controller Service in-cluster; tests
// point it at a local listener.
var sealedSecretsCertDialContext = (&net.Dialer{Timeout: sealedSecretsCertTimeout}).DialContext

type sealedSecretsCertController struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

type sealedSecretsCertResponse struct {
	CertPEM string `json:"certPem"`
	// CertFingerprint is the lowercase hex SHA-256 of the certificate's DER.
	CertFingerprint string                      `json:"certFingerprint"`
	Controller      sealedSecretsCertController `json:"controller"`
}

// handleSealedSecretsCert returns the public sealing certificate of the
// controller serving ?sealedSecretNamespace= (kubeseal's defaults decide
// without it), so a client can seal values itself, as `kubeseal --fetch-cert`
// does.
//
// The controller is chosen exactly as GET /api/sealedsecrets/controller
// chooses it, and never guessed: several candidates are 409 "ambiguous", a
// choice Radar can't make (an unreadable scope, Deployments not collected or
// failed to load) is 409 "unknown". The Service is the one named like the
// controller Deployment, as the standard installs name it, and the request
// is always a GET of sealedSecretsCertPath on its HTTP port: nothing in the
// request picks the Service, port or path. In-cluster Radar dials the
// Service directly; otherwise it goes through the API server's services/proxy
// as the caller. No redirects, a 5s timeout, a 64 KiB cap, and the answer
// must be one PEM CERTIFICATE carrying an RSA key (what kubeseal seals with),
// else 502. Only that certificate is returned, re-encoded.
//
// With auth enabled the caller needs get services/proxy on that Service by
// name, the grant kubeseal itself needs. Without auth (local, or a shared
// listener) the route reaches only that one path on that one Service, and
// returns a public certificate that upstream installs let every
// authenticated user fetch.
func (s *Server) handleSealedSecretsCert(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	target := r.URL.Query().Get("sealedSecretNamespace")
	if target != "" && len(validation.IsDNS1123Label(target)) > 0 {
		s.writeError(w, http.StatusBadRequest, "invalid namespace")
		return
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return
	}
	installed := false
	if d := k8s.GetResourceDiscovery(); d != nil {
		_, installed = d.GetGVRWithGroup("SealedSecret", "bitnami.com")
	}
	if !installed {
		s.writeErrorCode(w, http.StatusNotFound, "notInstalled", "Sealed Secrets is not installed on this cluster")
		return
	}

	choice := selectSealedSecretsController(cache, target)
	if choice.controller == nil {
		switch {
		case choice.ambiguousReason == ambiguousOverlap:
			s.writeErrorCode(w, http.StatusConflict, "ambiguous", "Several Sealed Secrets controllers serve this namespace; Radar can't tell which one unseals it")
		case choice.state == "loading":
			s.writeErrorCode(w, http.StatusServiceUnavailable, "loading", "Radar is still loading Deployments; try again shortly")
		case choice.state == "notFound" || choice.state == "notWatched":
			s.writeErrorCode(w, http.StatusNotFound, "notFound", "No Sealed Secrets controller serves this namespace")
		default:
			s.writeErrorCode(w, http.StatusConflict, "unknown", "Radar can't tell which Sealed Secrets controller serves this namespace")
		}
		return
	}
	ns, name := choice.controller.Namespace, choice.controller.Name

	allowed, authoritative := s.serviceProxyDecision(r, ns, name)
	if !authoritative {
		s.writeError(w, http.StatusServiceUnavailable, "Could not check your access to the controller; try again")
		return
	}
	if !allowed {
		s.writeErrorCode(w, http.StatusForbidden, "denied", "You don't have permission to fetch the controller's certificate (requires get services/proxy on its Service)")
		return
	}

	services := cache.Services()
	if services == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Service cache not available")
		return
	}
	svc, err := services.Services(ns).Get(name)
	if apierrors.IsNotFound(err) {
		s.writeErrorCode(w, http.StatusNotFound, "serviceNotFound", fmt.Sprintf("The controller has no Service named %s in %s", name, ns))
		return
	} else if err != nil {
		log.Printf("[sealedsecrets] Failed to read Service %s/%s: %v", ns, name, err)
		s.writeError(w, http.StatusInternalServerError, "Failed to read the controller's Service")
		return
	}
	port := sealedSecretsHTTPPort(svc)
	if port == 0 {
		s.writeErrorCode(w, http.StatusBadGateway, "upstream", "The controller's Service has no port named http and no single TCP port")
		return
	}

	client, url, err := sealedSecretsCertUpstream(r.Context(), ns, name, port)
	if err != nil {
		log.Printf("[sealedsecrets] Failed to build a client for %s/%s: %v", ns, name, err)
		s.writeError(w, http.StatusInternalServerError, "Failed to reach the controller")
		return
	}
	certPEM, fingerprint, fetchErr := fetchSealedSecretsCert(r.Context(), client, url)
	if fetchErr != nil {
		log.Printf("[sealedsecrets] Failed to fetch the certificate of %s/%s: %v", ns, name, fetchErr)
		s.writeErrorCode(w, http.StatusBadGateway, "upstream", "The controller did not return a usable certificate: "+fetchErr.public)
		return
	}
	s.writeJSON(w, sealedSecretsCertResponse{
		CertPEM:         certPEM,
		CertFingerprint: fingerprint,
		Controller:      sealedSecretsCertController{Namespace: ns, Name: name},
	})
}

// sealedSecretsHTTPPort is the TCP port named "http" (the charts' name), else
// the only TCP port (the upstream manifest leaves it unnamed); 0 otherwise.
func sealedSecretsHTTPPort(svc *corev1.Service) int32 {
	var tcp []int32
	for _, p := range svc.Spec.Ports {
		if p.Protocol != "" && p.Protocol != corev1.ProtocolTCP {
			continue
		}
		if p.Name == "http" {
			return p.Port
		}
		tcp = append(tcp, p.Port)
	}
	if len(tcp) == 1 {
		return tcp[0]
	}
	return 0
}

// sealedSecretsCertUpstream builds the client and URL for the certificate.
// In-cluster it dials the Service with a plain client that carries no
// Kubernetes credentials; otherwise it goes through the API server's
// services/proxy with the caller's identity (impersonated when signed in,
// the kubeconfig identity when not), so the API server enforces their RBAC.
func sealedSecretsCertUpstream(ctx context.Context, namespace, service string, port int32) (*http.Client, string, error) {
	noRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if k8s.IsInCluster() {
		return &http.Client{
			Timeout:       sealedSecretsCertTimeout,
			CheckRedirect: noRedirect,
			Transport: &http.Transport{
				DialContext:            sealedSecretsCertDialContext,
				DisableKeepAlives:      true,
				ResponseHeaderTimeout:  sealedSecretsCertTimeout,
				MaxResponseHeaderBytes: 16 << 10,
			},
		}, sealedSecretsCertDirectURL(namespace, service, port), nil
	}
	var cfg *rest.Config
	if user := auth.UserFromContext(ctx); user != nil {
		var err error
		if cfg, err = k8s.UserConfig(user); err != nil {
			return nil, "", err
		}
	} else if base := k8s.GetConfig(); base != nil {
		cfg = rest.CopyConfig(base)
	} else {
		return nil, "", fmt.Errorf("K8s config not initialized")
	}
	client, err := rest.HTTPClientFor(cfg)
	if err != nil {
		return nil, "", err
	}
	client.Timeout = sealedSecretsCertTimeout
	client.CheckRedirect = noRedirect
	url, err := sealedSecretsCertProxyURL(cfg, namespace, service, port)
	return client, url, err
}

func sealedSecretsCertDirectURL(namespace, service string, port int32) string {
	return "http://" + net.JoinHostPort(service+"."+namespace+".svc", strconv.Itoa(int(port))) + sealedSecretsCertPath
}

func sealedSecretsCertProxyURL(cfg *rest.Config, namespace, service string, port int32) (string, error) {
	base, _, err := rest.DefaultServerUrlFor(cfg)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(base.String(), "/") +
		"/api/v1/namespaces/" + namespace + "/services/http:" + service + ":" + strconv.Itoa(int(port)) + "/proxy" + sealedSecretsCertPath, nil
}

// certFetchError keeps what the caller is told apart from the logged cause,
// so nothing the upstream sent reaches the response.
type certFetchError struct {
	public string
	cause  error
}

func (e *certFetchError) Error() string {
	if e.cause == nil {
		return e.public
	}
	return e.public + ": " + e.cause.Error()
}

func certFetchFailed(public string, cause error) *certFetchError {
	return &certFetchError{public: public, cause: cause}
}

// fetchSealedSecretsCert GETs url and returns its first PEM block, re-encoded,
// and the SHA-256 of its DER, when that block is an X.509 certificate with an
// RSA public key.
func fetchSealedSecretsCert(ctx context.Context, client *http.Client, url string) (string, string, *certFetchError) {
	ctx, cancel := context.WithTimeout(ctx, sealedSecretsCertTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", certFetchFailed("invalid request", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", certFetchFailed("unreachable", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", certFetchFailed(fmt.Sprintf("status %d", resp.StatusCode), nil)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, sealedSecretsCertMaxBytes+1))
	if err != nil {
		return "", "", certFetchFailed("incomplete response", err)
	}
	if len(body) > sealedSecretsCertMaxBytes {
		return "", "", certFetchFailed(fmt.Sprintf("response over %d KiB", sealedSecretsCertMaxBytes>>10), nil)
	}
	block, _ := pem.Decode(body)
	if block == nil || block.Type != "CERTIFICATE" {
		return "", "", certFetchFailed("not a PEM certificate", nil)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", "", certFetchFailed("certificate does not parse", err)
	}
	if _, ok := cert.PublicKey.(*rsa.PublicKey); !ok {
		return "", "", certFetchFailed("not an RSA certificate", nil)
	}
	sum := sha256.Sum256(block.Bytes)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: block.Bytes})), hex.EncodeToString(sum[:]), nil
}

// serviceProxyDecision authorizes get services/proxy on one named Service,
// with the name in the review so grants limited by resourceNames apply.
// Without auth it allows: the API server or the fixed target bounds the read.
func (s *Server) serviceProxyDecision(r *http.Request, namespace, name string) (allowed, authoritative bool) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		return true, true
	}
	var perms *auth.UserPermissions
	if s.permCache != nil {
		perms = s.permCache.Get(user.Username, user.Groups)
	}
	key := "services/proxy/" + name
	if perms != nil {
		if v, ok := perms.CanI("get", "", key, namespace); ok {
			return v, true
		}
	}
	client := k8s.GetClient()
	if client == nil {
		return false, false
	}
	status, err := pkgauth.ReviewSubjectAccess(r.Context(), client, user.Username, user.Groups, authv1.ResourceAttributes{
		Verb: "get", Resource: "services", Subresource: "proxy", Namespace: namespace, Name: name,
	})
	if err != nil {
		log.Printf("[sealedsecrets] Failed to authorize services/proxy %s/%s: %v", k8s.SanitizeForLog(namespace), k8s.SanitizeForLog(name), err)
		return false, false
	}
	// An authorizer that allowed is final even if another one errored.
	authoritative = status.Allowed || status.EvaluationError == ""
	if authoritative && perms != nil {
		perms.SetCanI("get", "", key, namespace, status.Allowed)
	}
	return status.Allowed, authoritative
}
