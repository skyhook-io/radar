package server

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	veleropkg "github.com/skyhook-io/radar/pkg/velero"
)

// A Backup's status carries how many errors and warnings a run produced, and
// nothing else about them. The messages are written to a results file in object
// storage, and the only supported way to read that file is to ask Velero for it:
// create a DownloadRequest, and the controller answers with a pre-signed URL.
//
// A count with no way to reach what it counts is a dead end, so the messages
// have to be reachable from the page. Printing the `velero` command to run
// instead would be a first for this codebase — every CLI string in it is a
// comment, never rendered — whereas fetching on demand from a button is what
// the trace probes, the Argo diff and the Helm views already do.
//
// Two things this cannot do, both reported rather than hidden: it needs Velero's
// controller running (nothing else serves a DownloadRequest), and it needs the
// object storage the URL points at to be reachable from wherever Radar runs.

var downloadRequestGVR = schema.GroupVersionResource{
	Group: veleroGroup, Version: "v1", Resource: "downloadrequests",
}

// veleroResultTargets maps the route's kind to the DownloadRequest target that
// holds that kind's per-item messages. Deliberately a closed set: the enum has
// fourteen targets, and the rest are contents listings and volume metadata that
// answer a different question.
var veleroResultTargets = map[string]struct{ target, kind string }{
	"backups":  {target: "BackupResults", kind: "Backup"},
	"restores": {target: "RestoreResults", kind: "Restore"},
}

// VeleroMessage is one warning or error, with the scope Velero filed it under.
type VeleroMessage struct {
	// Scope is "velero", "cluster", or a namespace name — Velero's own three
	// buckets. Kept rather than flattened: "could not restore X" means something
	// different against a namespace than against the installation.
	Scope   string `json:"scope"`
	Message string `json:"message"`
}

// VeleroRunMessagesResponse is what a run actually reported.
type VeleroRunMessagesResponse struct {
	Warnings []VeleroMessage `json:"warnings"`
	Errors   []VeleroMessage `json:"errors"`
	// Truncated is set when the results file held more messages than are
	// returned. A silently capped list would read as the whole story.
	Truncated bool `json:"truncated,omitempty"`
}

// veleroResults is the on-disk shape Velero writes: two named result sets, each
// bucketed into velero-level, cluster-level and per-namespace messages.
type veleroResults struct {
	Velero     []string            `json:"velero"`
	Cluster    []string            `json:"cluster"`
	Namespaces map[string][]string `json:"namespaces"`
}

// The failure modes of reading the results file, as sentinels rather than
// strings. The reader is sent somewhere different by each one, and the handler
// needs to distinguish them without matching on error text.
var (
	errStorageUnreachable  = errors.New("the object store did not answer")
	errStorageRefused      = errors.New("the object store refused the request")
	errStorageRedirected   = errors.New("the object store redirected, which is not followed")
	errNotAResultsFile     = errors.New("what came back is not a Velero results file")
	errDownloadDestination = errors.New("download destination is not allowed")
	errMalformedURL        = errors.New("Velero returned a URL that cannot be parsed")
)

// urlSafeCause strips the URL out of an HTTP client error.
//
// The URL here is Velero's pre-signed one, and a pre-signed URL IS the
// credential: anything holding it can read the results file until it expires.
// Go wraps transport failures in *url.Error, whose Error() embeds the full URL
// — so returning or logging that error as-is publishes the credential to
// whoever can read a response body or a log line. Only the underlying cause
// travels.
func urlSafeCause(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err
	}
	return err
}

const (
	// The controller normally answers in under a second. This bounds the wait
	// for the case that matters — a controller that is not going to answer at
	// all — without giving up on a busy one.
	downloadRequestTimeout = 20 * time.Second
	// Bounds the fetch from object storage. The results file is small; anything
	// approaching this is not a results file.
	maxResultsBytes = 8 << 20
	maxMessages     = 500
)

// handleVeleroRunMessages returns the warnings and errors behind a run's counts.
//
//	POST /api/velero/{kind}/{namespace}/{name}/messages     kind: backups|restores
//
// POST because it creates a DownloadRequest. Impersonated, so the apiserver
// decides whether this caller may ask — a reader who cannot create
// DownloadRequests gets the apiserver's own answer rather than Radar's guess.
func (s *Server) handleVeleroRunMessages(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	kindParam := strings.ToLower(chi.URLParam(r, "kind"))
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	target, ok := veleroResultTargets[kindParam]
	if !ok {
		s.writeError(w, http.StatusBadRequest, "messages are available for backups and restores")
		return
	}
	if namespace == "" || name == "" {
		s.writeError(w, http.StatusBadRequest, "namespace and name are required")
		return
	}
	if s.refuseOutsideVeleroPin(w, namespace) {
		return
	}

	// Creating a DownloadRequest is the mechanism, not the permission being
	// asked about. The apiserver will enforce `create downloadrequests` below,
	// but that is a grant Velero's own CLI needs and is often held broadly —
	// on its own it would let someone read the errors of a Backup they cannot
	// open. Gate on the run itself, the way the sibling lookup gates on
	// listing Backups.
	if !s.canRead(r, veleroGroup, kindParam, namespace, "get") {
		s.writeError(w, http.StatusForbidden, "no access to this Velero "+target.kind)
		return
	}

	// Only the Velero server answers a DownloadRequest. When the caller can see
	// that it is not running, say so now rather than after the full wait.
	if deployments, read := s.veleroDeployments(r, k8s.GetResourceCache(), namespace); read {
		if server := veleropkg.ServerConfigFromDeployments(deployments, true); server.Down() {
			s.writeError(w, http.StatusServiceUnavailable,
				fmt.Sprintf("The Velero server (Deployment %s in namespace %s) has no running replicas, and only it can serve these messages. Start it, then try again.",
					server.Deployment, namespace))
			return
		}
	}

	auth.AuditLog(r, namespace, name)
	client := s.getDynamicClientForRequest(r)
	if client == nil {
		log.Printf("[velero] Dynamic client unavailable for %s messages %s/%s",
			target.kind, sanitizeForLog(namespace), sanitizeForLog(name))
		s.writeError(w, http.StatusServiceUnavailable, "dynamic client not available")
		return
	}

	req := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": veleroGroup + "/v1",
		"kind":       "DownloadRequest",
		"metadata": map[string]interface{}{
			// generateName, not a name built here: two readers opening the same
			// backup at once would otherwise collide on AlreadyExists.
			"generateName": name + "-",
			"namespace":    namespace,
		},
		"spec": map[string]interface{}{
			"target": map[string]interface{}{"kind": target.target, "name": name},
		},
	}}

	created, err := client.Resource(downloadRequestGVR).Namespace(namespace).
		Create(r.Context(), req, metav1.CreateOptions{})
	switch {
	case err == nil:
	case apierrors.IsForbidden(err):
		s.writeError(w, http.StatusForbidden, "no access to create Velero download requests")
		return
	case apierrors.IsNotFound(err):
		// No DownloadRequest CRD — Velero is not installed, or predates it.
		s.writeError(w, http.StatusNotFound, "this cluster has no Velero download requests")
		return
	default:
		log.Printf("[velero] Failed to create DownloadRequest for %s %s/%s: %v",
			target.kind, sanitizeForLog(namespace), sanitizeForLog(name), err)
		s.writeError(w, http.StatusServiceUnavailable, "could not ask Velero for the messages")
		return
	}

	// Best-effort cleanup. Velero garbage-collects processed DownloadRequests on
	// its own TTL, so a leaked one is untidy rather than harmful — but only if
	// the controller is running, which is exactly what is in doubt here.
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = client.Resource(downloadRequestGVR).Namespace(namespace).
			Delete(ctx, created.GetName(), metav1.DeleteOptions{})
	}()

	downloadURL, err := awaitDownloadURL(r.Context(), client, namespace, created.GetName(), downloadRequestTimeout)
	if err != nil {
		log.Printf("[velero] No download URL for %s %s/%s: %v",
			target.kind, sanitizeForLog(namespace), sanitizeForLog(name), err)
		// A denial is not a silent controller. Reading back the DownloadRequest
		// needs `get downloadrequests`, and a caller who can create but not get
		// them would otherwise be told Velero is down — sending them to look at
		// a controller that is running fine.
		if apierrors.IsForbidden(err) {
			s.writeError(w, http.StatusForbidden, "no access to read Velero download requests")
			return
		}
		// 503 rather than 500: nothing is wrong with the request, something in
		// the cluster is not answering, and the message says which.
		// The namespace is worth naming and the workload is not: the run's own
		// namespace is where Velero is installed, but the controller's name
		// varies by distribution (OADP does not call it `velero`), so pointing
		// at one would be a guess dressed as a fact.
		s.writeError(w, http.StatusServiceUnavailable,
			fmt.Sprintf("Velero did not answer within %s. These messages are served by the Velero controller in namespace %s — check that it is running.",
				downloadRequestTimeout, namespace))
		return
	}

	allowLoopback := !k8s.IsInCluster() && !s.sharedListener()
	results, err := fetchVeleroResults(r.Context(), downloadURL, func(ip net.IP) bool { return veleroDownloadAddressAllowed(ip, allowLoopback) })
	if err != nil {
		log.Printf("[velero] Failed to fetch results for %s %s/%s: %v",
			target.kind, sanitizeForLog(namespace), sanitizeForLog(name), err)
		// Which failure it was decides where the reader goes, so they are
		// distinguished — but from sentinels, never by pasting the error in.
		// The error can carry Velero's pre-signed URL, which is a credential
		// for the results file, and a response body is not a place to publish
		// one.
		s.writeError(w, http.StatusServiceUnavailable, veleroFetchMessage(err))
		return
	}
	s.writeJSON(w, results)
}

// veleroFetchMessage turns a results-fetch failure into what the reader should
// do about it. Each branch sends them somewhere different: the object store is
// a different thing to check from the Velero controller, and a store that
// answered with the wrong bytes is different again.
func veleroFetchMessage(err error) string {
	const reach = "Radar reaches the object store directly, so it has to be reachable from wherever Radar runs."
	switch {
	case errors.Is(err, errDownloadDestination):
		return "Radar refused the messages download destination: link-local, unspecified and multicast addresses are never allowed, and loopback is allowed only on a local private listener."
	case errors.Is(err, errStorageUnreachable):
		return "Velero returned a link to the messages, but the object store did not answer. " + reach
	case errors.Is(err, errStorageRefused):
		return "Velero returned a link to the messages, but the object store refused it. " +
			"A pre-signed link is only valid for a short window, and the credentials Velero signed it with must still be valid."
	case errors.Is(err, errStorageRedirected):
		return "Velero returned a link to the messages, but the object store redirected it. " +
			"Radar does not follow redirects on a signed link, because that is how a link reaches a host nobody named."
	case errors.Is(err, errNotAResultsFile):
		return "Velero returned a link to the messages, but what came back is not a results file."
	case errors.Is(err, errMalformedURL):
		return "Velero returned a link to the messages that Radar could not parse."
	default:
		return "Velero returned a link to the messages, but Radar could not read it. " + reach
	}
}

// awaitDownloadURL polls the DownloadRequest until the controller fills in the
// URL. Watch would be tidier; this handler is already bounded and a poll keeps
// the failure mode ("nothing ever answered") trivially readable.
func awaitDownloadURL(ctx context.Context, client dynamic.Interface, namespace, name string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for {
		got, err := client.Resource(downloadRequestGVR).Namespace(namespace).
			Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", err
		}
		if u, _, _ := unstructured.NestedString(got.Object, "status", "downloadURL"); u != "" {
			return u, nil
		}
		if time.Now().After(deadline) {
			phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
			if phase == "" {
				phase = "no status at all"
			}
			return "", fmt.Errorf("download request still %s after %s", phase, timeout)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// Signed object-store links must answer directly. Redirects and proxies could
// reach a destination that the dial-time policy never checked.
func fetchVeleroResults(ctx context.Context, rawURL string, destinationAllowed func(net.IP) bool) (VeleroRunMessagesResponse, error) {
	var out VeleroRunMessagesResponse

	parsed, err := url.Parse(rawURL)
	if err != nil {
		// Deliberately not wrapping err: url.Parse embeds the URL it failed on.
		return out, errMalformedURL
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return out, fmt.Errorf("refusing to fetch a %q URL", parsed.Scheme)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		// url.Parse above rejects the same inputs, so nothing is known to reach
		// here — but this is the last path in the function that could return an
		// error carrying the URL, and the guarantee is worth more than the one
		// line it costs. NewRequest wraps its parse failure in *url.Error.
		return out, errMalformedURL
	}
	dialer := &net.Dialer{Timeout: 30 * time.Second, Control: func(network, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil || !destinationAllowed(net.ParseIP(host)) {
			return errDownloadDestination
		}
		return nil
	}}
	transport := &http.Transport{DialContext: dialer.DialContext, TLSHandshakeTimeout: 10 * time.Second, ForceAttemptHTTP2: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, errDownloadDestination) {
			return out, errDownloadDestination
		}
		return out, fmt.Errorf("%w: %v", errStorageUnreachable, urlSafeCause(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		// The Location is not repeated: a redirect target can itself be a signed
		// URL, and it is chosen by whatever answered rather than by us.
		return out, errStorageRedirected
	}
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("%w: %s", errStorageRefused, resp.Status)
	}

	gz, err := gzip.NewReader(io.LimitReader(resp.Body, maxResultsBytes))
	if err != nil {
		return out, fmt.Errorf("%w: not gzip", errNotAResultsFile)
	}
	defer gz.Close()

	var raw map[string]veleroResults
	if err := json.NewDecoder(io.LimitReader(gz, maxResultsBytes)).Decode(&raw); err != nil {
		return out, fmt.Errorf("%w: unreadable", errNotAResultsFile)
	}

	out.Warnings, out.Truncated = flattenVeleroResult(raw["warnings"])
	errs, truncated := flattenVeleroResult(raw["errors"])
	out.Errors = errs
	out.Truncated = out.Truncated || truncated
	return out, nil
}

// flattenVeleroResult turns Velero's three buckets into one ordered list,
// keeping the bucket as the message's scope.
func flattenVeleroResult(r veleroResults) ([]VeleroMessage, bool) {
	out := make([]VeleroMessage, 0, len(r.Velero)+len(r.Cluster))
	add := func(scope string, msgs []string) {
		for _, m := range msgs {
			out = append(out, VeleroMessage{Scope: scope, Message: m})
		}
	}
	add("velero", r.Velero)
	add("cluster", r.Cluster)

	// Map iteration order is random, and a list that reshuffles on every click
	// reads as changing data.
	namespaces := make([]string, 0, len(r.Namespaces))
	for ns := range r.Namespaces {
		namespaces = append(namespaces, ns)
	}
	sort.Strings(namespaces)
	for _, ns := range namespaces {
		add(ns, r.Namespaces[ns])
	}

	if len(out) > maxMessages {
		return out[:maxMessages], true
	}
	return out, false
}

// Control receives the resolved dial address, so DNS changes cannot bypass this
// check. The transport does not use proxies, which would hide that destination.
func veleroDownloadAddressAllowed(ip net.IP, allowLoopback bool) bool {
	return ip != nil && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() && !ip.IsUnspecified() && !ip.IsMulticast() && (allowLoopback || !ip.IsLoopback())
}
