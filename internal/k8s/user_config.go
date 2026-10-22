package k8s

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	authv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

// UserRESTConfig returns the config Radar uses for a user's Kubernetes calls.
// In impersonate mode it impersonates the user. In user-token mode it never
// does: a user with a token gets a config carrying only that token, an
// allow-listed system identity gets Radar's own credentials behind a
// SubjectAccessReview of every request, and anyone else gets a config whose
// every request is refused.
func UserRESTConfig(base *rest.Config, user *pkgauth.User) *rest.Config {
	if !pkgauth.UserTokenMode() {
		return pkgauth.ImpersonatedConfig(base, user.Username, user.Groups)
	}
	if user.Token != "" {
		cfg := rest.AnonymousClientConfig(base)
		cfg.BearerToken = user.Token
		return cfg
	}
	cfg := rest.CopyConfig(base)
	if user.System {
		reviewer := subjectReviewer(base)
		prefix := ""
		if u, err := url.Parse(base.Host); err == nil {
			prefix = strings.TrimSuffix(u.Path, "/")
		}
		cfg.Wrap(func(rt http.RoundTripper) http.RoundTripper {
			return &systemReviewTransport{next: rt, user: user, reviewer: reviewer, pathPrefix: prefix}
		})
		return cfg
	}
	cfg.Wrap(func(http.RoundTripper) http.RoundTripper { return userTokenRequiredTransport{} })
	return cfg
}

var requestInfoFactory = &request.RequestInfoFactory{
	APIPrefixes:          sets.NewString("api", "apis"),
	GrouplessAPIPrefixes: sets.NewString("api"),
}

var (
	streamSubresources = sets.NewString("exec", "attach", "portforward")
	readVerbs          = sets.NewString("get", "list", "watch")
)

const systemReviewTTL = time.Minute

type systemReviewTransport struct {
	next       http.RoundTripper
	user       *pkgauth.User
	reviewer   *cachedSubjectReviewer
	pathPrefix string // a proxy path in front of the API server, e.g. /k8s/clusters/c-1
}

func (t *systemReviewTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	parse := req
	if t.pathPrefix != "" && strings.HasPrefix(req.URL.Path, t.pathPrefix+"/") {
		parse = req.Clone(req.Context())
		parse.URL.Path = strings.TrimPrefix(req.URL.Path, t.pathPrefix)
	}
	info, err := requestInfoFactory.NewRequestInfo(parse)
	if err != nil {
		return deny(req, "Radar could not authorize this request: "+err.Error(), metav1.StatusReasonForbidden)
	}
	// The API server adds system:authenticated to every impersonated user;
	// a SubjectAccessReview checks only the groups it is given, and discovery
	// and /version are granted through that group.
	spec := authv1.SubjectAccessReviewSpec{User: t.user.Username, Groups: append(append([]string(nil), t.user.Groups...), "system:authenticated")}
	if info.IsResourceRequest {
		verb := info.Verb
		if streamSubresources.Has(info.Subresource) {
			// Exec, attach and port-forward over WebSocket arrive as GET; the
			// API server also requires create for them.
			verb = "create"
		}
		spec.ResourceAttributes = &authv1.ResourceAttributes{
			Namespace:   info.Namespace,
			Verb:        verb,
			Group:       info.APIGroup,
			Version:     info.APIVersion,
			Resource:    info.Resource,
			Subresource: info.Subresource,
			Name:        info.Name,
		}
	} else {
		spec.NonResourceAttributes = &authv1.NonResourceAttributes{Path: info.Path, Verb: info.Verb}
	}
	if !readVerbs.Has(reviewSpec(spec).verb()) {
		// Background identities only read. The request would run as Radar's
		// ServiceAccount, so a write would be judged by admission and RBAC
		// escalation checks as the ServiceAccount, not as this identity.
		return deny(req, t.user.Username+" may only read through Radar", metav1.StatusReasonForbidden)
	}
	allowed, err := t.reviewer.allowed(req.Context(), spec)
	if err != nil {
		log.Printf("[auth] SubjectAccessReview for %s failed: %v", SanitizeForLog(t.user.Username), err)
		return denyStatus(req, http.StatusServiceUnavailable, "Radar could not check permissions for "+t.user.Username+" with the API server", metav1.StatusReasonServiceUnavailable)
	}
	if !allowed {
		return deny(req, fmt.Sprintf("%s is not allowed to %s this resource", t.user.Username, reviewSpec(spec).verb()), metav1.StatusReasonForbidden)
	}
	return t.next.RoundTrip(req)
}

const userTokenRequiredMessage = "Live actions need a user token, which only single sign-on sessions carry; this request has none."

// IsUserTokenRequired reports whether msg carries the refusal of a live call
// made without a user token. It matches the text because handlers and Helm
// flatten the API error into their own messages.
func IsUserTokenRequired(msg string) bool {
	return strings.Contains(msg, userTokenRequiredMessage)
}

type userTokenRequiredTransport struct{}

func (userTokenRequiredTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return deny(req, userTokenRequiredMessage, metav1.StatusReason(pkgauth.UserTokenRequiredReason))
}

// deny refuses a request without sending it. A REST call gets the API
// server's own 403 Status shape, so client-go surfaces an ordinary
// StatusError. A stream upgrade (exec, attach, port-forward) gets the error
// itself: an upgrader handed a response it never negotiated reports success
// with no connection, and client-go's WebSocket executor panics on that. The
// WebSocket dialer adds its Upgrade headers below this wrapper, so the
// subprotocol header is what marks its requests here.
func deny(req *http.Request, message string, reason metav1.StatusReason) (*http.Response, error) {
	return denyStatus(req, http.StatusForbidden, message, reason)
}

func denyStatus(req *http.Request, code int, message string, reason metav1.StatusReason) (*http.Response, error) {
	status := metav1.Status{
		TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"},
		Status:   metav1.StatusFailure,
		Message:  message,
		Reason:   reason,
		Code:     int32(code),
	}
	if httpstream.IsUpgradeRequest(req) || req.Header.Get("Sec-WebSocket-Protocol") != "" {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, &apierrors.StatusError{ErrStatus: status}
	}
	return forbiddenResponse(req, status), nil
}

func forbiddenResponse(req *http.Request, status metav1.Status) *http.Response {
	body, _ := json.Marshal(status)
	if req.Body != nil {
		_ = req.Body.Close()
	}
	return &http.Response{
		StatusCode:    int(status.Code),
		Status:        fmt.Sprintf("%d %s", status.Code, http.StatusText(int(status.Code))),
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       req,
	}
}

type cachedSubjectReviewer struct {
	client kubernetes.Interface
	err    error

	mu      sync.Mutex
	entries map[string]subjectReviewEntry
}

type subjectReviewEntry struct {
	allowed bool
	expires time.Time
}

var (
	subjectReviewersMu sync.Mutex
	subjectReviewers   = map[string]*cachedSubjectReviewer{}
)

// subjectReviewer returns one reviewer per API server, so decisions are
// cached across the many short-lived clients built per request.
func subjectReviewer(base *rest.Config) *cachedSubjectReviewer {
	subjectReviewersMu.Lock()
	defer subjectReviewersMu.Unlock()
	key := reviewerKey(base)
	if r, ok := subjectReviewers[key]; ok && r.err == nil {
		return r
	}
	client, err := kubernetes.NewForConfig(base)
	r := &cachedSubjectReviewer{client: client, err: err, entries: map[string]subjectReviewEntry{}}
	if len(subjectReviewers) > 8 {
		subjectReviewers = map[string]*cachedSubjectReviewer{}
	}
	subjectReviewers[key] = r
	return r
}

// reviewerKey names the API server and the credentials Radar reviews with,
// so a context switch to other credentials on the same server gets its own
// reviewer.
func reviewerKey(base *rest.Config) string {
	h := sha256.New()
	for _, part := range []string{base.Host, base.BearerToken, base.BearerTokenFile, base.CertFile, string(base.CertData), base.Username} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (r *cachedSubjectReviewer) allowed(ctx context.Context, spec authv1.SubjectAccessReviewSpec) (bool, error) {
	if r.err != nil {
		return false, r.err
	}
	key := subjectReviewKey(spec)
	now := time.Now()
	r.mu.Lock()
	if e, ok := r.entries[key]; ok && now.Before(e.expires) {
		r.mu.Unlock()
		return e.allowed, nil
	}
	r.mu.Unlock()

	result, err := r.client.AuthorizationV1().SubjectAccessReviews().Create(ctx, &authv1.SubjectAccessReview{Spec: spec}, metav1.CreateOptions{})
	if err != nil {
		return false, err
	}
	r.mu.Lock()
	if len(r.entries) > 4096 {
		r.entries = map[string]subjectReviewEntry{}
	}
	r.entries[key] = subjectReviewEntry{allowed: result.Status.Allowed, expires: now.Add(systemReviewTTL)}
	r.mu.Unlock()
	return result.Status.Allowed, nil
}

type reviewSpec authv1.SubjectAccessReviewSpec

func (s reviewSpec) verb() string {
	if s.ResourceAttributes != nil {
		return s.ResourceAttributes.Verb
	}
	if s.NonResourceAttributes != nil {
		return s.NonResourceAttributes.Verb
	}
	return ""
}

func subjectReviewKey(spec authv1.SubjectAccessReviewSpec) string {
	groups := append([]string(nil), spec.Groups...)
	sort.Strings(groups)
	parts := []string{spec.User, strings.Join(groups, ",")}
	if a := spec.ResourceAttributes; a != nil {
		parts = append(parts, "r", a.Namespace, a.Verb, a.Group, a.Version, a.Resource, a.Subresource, a.Name)
	}
	if a := spec.NonResourceAttributes; a != nil {
		parts = append(parts, "n", a.Path, a.Verb)
	}
	return strings.Join(parts, "\x00")
}
