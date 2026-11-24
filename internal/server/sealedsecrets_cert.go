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
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilnet "k8s.io/apimachinery/pkg/util/net"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes"
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
// must be a current PEM CERTIFICATE carrying an RSA key (what kubeseal seals with),
// else 502. Only that certificate is returned, re-encoded.
//
// With auth enabled the caller needs get services/proxy on the same
// Service-and-port name that kubeseal uses. Without auth (local, or a shared
// listener) the route reaches only that one path on that one Service, and
// returns a public certificate that upstream installs let every
// authenticated user fetch.
func (s *Server) handleSealedSecretsCert(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	user := auth.UserFromContext(r.Context())
	if user != nil && pkgauth.UserTokenMode() && !user.System && user.Token == "" {
		s.writeError(w, http.StatusForbidden, k8s.UserTokenRequiredMessage)
		return
	}
	target := r.URL.Query().Get("sealedSecretNamespace")
	if target != "" && len(validation.IsDNS1123Label(target)) > 0 {
		s.writeError(w, http.StatusBadRequest, "invalid namespace")
		return
	}
	conn := k8s.Current()
	cache, discovery, dynamic := conn.ResourceCache(), conn.Discovery(), conn.DynamicResourceCache()
	if cache == nil || discovery == nil {
		s.writeErrorCode(w, http.StatusServiceUnavailable, string(readreason.KindSyncPending), "Sealed Secrets discovery is not ready; retry shortly")
		return
	}
	if _, installed := discovery.GetGVRWithGroup("SealedSecret", "bitnami.com"); !installed {
		switch {
		case dynamic == nil || dynamic.GetDiscoveryStatus() != k8score.CRDDiscoveryComplete:
			s.writeErrorCode(w, http.StatusServiceUnavailable, string(readreason.KindSyncPending), "Sealed Secrets discovery is not ready; retry shortly")
		case discovery.LastRefreshFailed() || discovery.GroupHadPartialDiscovery("bitnami.com"):
			discovery.RetryFailedRefresh()
			s.writeErrorCode(w, http.StatusServiceUnavailable, string(readreason.ReadError), "Sealed Secrets discovery is incomplete; retry shortly")
		default:
			s.writeErrorCode(w, http.StatusNotFound, "notInstalled", "Sealed Secrets is not installed on this cluster")
		}
		return
	}

	coverage := cache.AssessKindRead("deployments", nil)
	if !coverage.Complete {
		s.writeErrorCode(w, http.StatusServiceUnavailable, string(readreason.KindSyncPending), "Radar does not have a complete view of Deployments; check its collection scope and permissions")
		return
	}
	choice := selectSealedSecretsController(cache, target, true)
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

	object, found, readable := cache.ReadableObject("services", ns, name)
	if !readable {
		s.writeErrorCode(w, http.StatusServiceUnavailable, string(readreason.KindSyncPending), "Radar cannot read the controller's Service; check its collection scope and permissions")
		return
	}
	if !found {
		s.writeErrorCode(w, http.StatusNotFound, "serviceNotFound", "The controller has no matching Service")
		return
	}
	svc := object.(*corev1.Service)
	port := sealedSecretsHTTPPort(svc)
	if port == nil {
		s.writeErrorCode(w, http.StatusBadGateway, "upstream", "The controller's Service has no port named http and no single TCP port")
		return
	}
	proxyName := utilnet.JoinSchemeNamePort("http", name, port.Name)
	allowed, err := sealingServiceProxyAllowed(r.Context(), conn, user, ns, proxyName)
	if err != nil {
		s.writeErrorCode(w, http.StatusServiceUnavailable, string(readreason.UserPermissionCheckFailed), "Could not check your access to the controller; try again")
		return
	}
	if !allowed {
		s.writeErrorCode(w, http.StatusForbidden, "denied", "You don't have permission to fetch the controller's certificate (requires get services/proxy on its Service)")
		return
	}
	if svc.Spec.Type == corev1.ServiceTypeExternalName || len(svc.Spec.Selector) == 0 {
		s.writeErrorCode(w, http.StatusBadGateway, "upstream", "The controller's Service must select pods in its namespace")
		return
	}

	client, url, err := sealedSecretsCertUpstream(conn, user, ns, name, port.Port, proxyName)
	if err != nil {
		log.Printf("[sealedsecrets] Failed to build a client for %s/%s: %v", ns, name, err)
		s.writeError(w, http.StatusInternalServerError, "Failed to reach the controller")
		return
	}
	certPEM, fingerprint, fetchErr := fetchSealedSecretsCert(r.Context(), client, url)
	if k8s.Current() != conn {
		s.writeErrorCode(w, http.StatusConflict, string(readreason.ConnectionChanged), "Cluster connection changed; retry")
		return
	}
	if fetchErr != nil {
		if fetchErr.forbidden {
			s.writeErrorCode(w, http.StatusForbidden, "denied", "You don't have permission to fetch the controller's certificate")
			return
		}
		log.Printf("[sealedsecrets] Failed to fetch the certificate of %s/%s: %v", ns, name, fetchErr)
		s.writeErrorCode(w, http.StatusBadGateway, "upstream", "The controller did not return a usable certificate: "+fetchErr.public)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.writeJSON(w, sealedSecretsCertResponse{
		CertPEM:         certPEM,
		CertFingerprint: fingerprint,
		Controller:      sealedSecretsCertController{Namespace: ns, Name: name},
	})
}

// sealedSecretsHTTPPort is the TCP port named "http" (the charts' name), else
// the only TCP port (the upstream manifest leaves it unnamed).
func sealedSecretsHTTPPort(svc *corev1.Service) *corev1.ServicePort {
	var onlyTCP *corev1.ServicePort
	var count int
	for i := range svc.Spec.Ports {
		p := &svc.Spec.Ports[i]
		if p.Protocol != "" && p.Protocol != corev1.ProtocolTCP {
			continue
		}
		if p.Name == "http" {
			return p
		}
		onlyTCP = p
		count++
	}
	if count == 1 {
		return onlyTCP
	}
	return nil
}

// sealedSecretsCertUpstream builds the client and URL for the certificate.
// In-cluster it dials the Service with a plain client that carries no
// Kubernetes credentials; otherwise it goes through the API server's
// services/proxy with the caller's identity (impersonated when signed in,
// the kubeconfig identity when not), so the API server enforces their RBAC.
func sealedSecretsCertUpstream(conn *k8s.Connection, user *auth.User, namespace, service string, port int32, proxyName string) (*http.Client, string, error) {
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
	cfg := conn.ConfigCopy()
	if cfg == nil {
		return nil, "", fmt.Errorf("K8s config not initialized")
	}
	if user != nil {
		cfg = k8s.UserRESTConfig(cfg, user)
	}
	client, err := rest.HTTPClientFor(cfg)
	if err != nil {
		return nil, "", err
	}
	client.Timeout = sealedSecretsCertTimeout
	client.CheckRedirect = noRedirect
	url, err := sealedSecretsCertProxyURL(cfg, namespace, proxyName)
	return client, url, err
}

func sealedSecretsCertDirectURL(namespace, service string, port int32) string {
	return "http://" + net.JoinHostPort(service+"."+namespace+".svc", strconv.Itoa(int(port))) + sealedSecretsCertPath
}

func sealedSecretsCertProxyURL(cfg *rest.Config, namespace, proxyName string) (string, error) {
	base, _, err := rest.DefaultServerUrlFor(cfg)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(base.String(), "/") +
		"/api/v1/namespaces/" + namespace + "/services/" + proxyName + "/proxy" + sealedSecretsCertPath, nil
}

// certFetchError keeps what the caller is told apart from the logged cause,
// so nothing the upstream sent reaches the response.
type certFetchError struct {
	public    string
	cause     error
	forbidden bool
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
	if resp.StatusCode == http.StatusForbidden {
		return "", "", &certFetchError{public: "access denied", forbidden: true}
	}
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
	now := time.Now()
	if now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
		return "", "", certFetchFailed("certificate is outside its validity period", nil)
	}
	sum := sha256.Sum256(block.Bytes)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: block.Bytes})), hex.EncodeToString(sum[:]), nil
}

// sealingServiceProxyAllowed authorizes get services/proxy on one named Service,
// with the name in the review so grants limited by resourceNames apply.
// Without auth it allows: the API server or the fixed target bounds the read.
func sealingServiceProxyAllowed(ctx context.Context, conn *k8s.Connection, user *auth.User, namespace, name string) (bool, error) {
	if user == nil {
		return true, nil
	}
	ctx, cancel := context.WithTimeout(ctx, sealedSecretsCertTimeout)
	defer cancel()
	attrs := authv1.ResourceAttributes{
		Verb: "get", Resource: "services", Subresource: "proxy", Namespace: namespace, Name: name,
	}
	var status authv1.SubjectAccessReviewStatus
	if pkgauth.UserTokenMode() && !user.System {
		cfg := conn.Config()
		if cfg == nil {
			return false, fmt.Errorf("K8s config not initialized")
		}
		client, err := kubernetes.NewForConfig(k8s.UserRESTConfig(cfg, user))
		if err != nil {
			return false, err
		}
		review, err := client.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authv1.SelfSubjectAccessReview{
			Spec: authv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &attrs},
		}, metav1.CreateOptions{})
		if err != nil {
			return false, err
		}
		status = review.Status
	} else {
		client := conn.ClientInterface()
		if client == nil {
			return false, fmt.Errorf("K8s client not initialized")
		}
		groups := user.Groups
		// Kubernetes includes this group for an impersonated user.
		if !slices.Contains(groups, "system:authenticated") {
			groups = append(slices.Clone(groups), "system:authenticated")
		}
		var err error
		status, err = pkgauth.ReviewSubjectAccess(ctx, client, user.Username, groups, attrs)
		if err != nil {
			return false, err
		}
	}
	// An authorizer that allowed is final even if another one errored.
	if !status.Allowed && status.EvaluationError != "" {
		return false, fmt.Errorf("Kubernetes authorization evaluation failed")
	}
	return status.Allowed, nil
}
