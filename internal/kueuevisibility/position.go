// Package kueuevisibility reads where Kueue places a pending Workload in its
// queues, from Kueue's own visibility API (visibility.kueue.x-k8s.io). Radar
// never derives a position from list order or timestamps: when the API is
// absent, refused or unreadable, the answer says so.
//
// Reads go to the LocalQueue subresource only. It is namespaced, so a person
// who may read one namespace's pending order learns nothing about another
// namespace except how many Workloads are ahead in the shared ClusterQueue,
// which is what Kueue itself authorizes. The ClusterQueue subresource of
// Kueue v0.18.0–v0.18.10 and v0.19.0–v0.19.1 also miscounts
// positionInLocalQueue when two namespaces use the same LocalQueue name.
package kueuevisibility

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
)

const (
	// Group is Kueue's aggregated visibility API group.
	Group = "visibility.kueue.x-k8s.io"
	// RowLimit is how many pending Workloads Radar asks for: Kueue's own
	// default page size.
	RowLimit = 1000
	// maxBodyBytes bounds what Radar buffers; a full page of rows with owner
	// references stays well under it.
	maxBodyBytes = 4 << 20
	// ReadTimeout bounds one read of Kueue's API.
	ReadTimeout = 4 * time.Second
	// memoTTL is how long one identity's answer for one LocalQueue is reused:
	// every read makes Kueue copy and sort the whole ClusterQueue.
	memoTTL = 10 * time.Second
)

// supportedVersions are the served versions whose PendingWorkloadsSummary
// shape Radar verified (Kueue v0.18–v0.20), most preferred first. Discovery
// drops subresources, so a served parent localqueues resource stands in for
// its pendingworkloads subresource; a 404 for the subresource path still
// reads as unavailable, never as an answer.
var supportedVersions = []string{"v1beta2", "v1beta1"}

// Discovery is the part of Radar's discovery a read consults. It never
// refreshes discovery, so a read can't stall on a full rediscovery.
type Discovery interface {
	Catalog() k8score.DiscoveryCatalog
	GroupHadPartialDiscovery(group string) bool
	LastRefreshFailed() bool
}

// Identity is whose access a read runs with, which decides whose access a
// refusal names.
type Identity int

const (
	// IdentityRadar: Radar's own Kubernetes identity (no signed-in user).
	IdentityRadar Identity = iota
	// IdentityUser: a signed-in user, impersonated or with their own token.
	IdentityUser
	// IdentitySystem: an allow-listed background identity; Radar checks its
	// access with a SubjectAccessReview, then reads with its own credentials.
	IdentitySystem
)

func IdentityOf(user *pkgauth.User) Identity {
	switch {
	case user == nil:
		return IdentityRadar
	case user.System && pkgauth.UserTokenMode():
		return IdentitySystem
	default:
		return IdentityUser
	}
}

type Request struct {
	Workload *unstructured.Unstructured
	// Client reads as Identity. Nil means Radar could not build the
	// caller's client, and the read fails closed.
	Client    kubernetes.Interface
	Discovery Discovery
	Identity  Identity
	// MemoKey scopes reuse of an answer: the cluster connection and the
	// identity reading it. Empty disables reuse.
	MemoKey string
}

// Reader reads positions, reusing one identity's answer for one LocalQueue
// for a few seconds.
type Reader struct {
	now      func() time.Time
	ttl      time.Duration
	timeout  time.Duration
	inflight singleflight.Group
	mu       sync.Mutex
	memo     map[string]fetched
}

func NewReader() *Reader {
	return &Reader{now: time.Now, ttl: memoTTL, timeout: ReadTimeout, memo: map[string]fetched{}}
}

// fetched is one LocalQueue answer: rows, or the answer to give instead.
type fetched struct {
	rows    []pendingWorkload
	failure *resourcecontext.KueuePendingPosition
	readAt  time.Time
}

// Read asks Kueue's visibility API for the Workload's position in its
// LocalQueue. It always returns an answer; failures are statuses, not errors.
func (r *Reader) Read(ctx context.Context, req Request) *resourcecontext.KueuePendingPosition {
	queue, _, _ := unstructured.NestedString(req.Workload.Object, "spec", "queueName")
	out := &resourcecontext.KueuePendingPosition{LocalQueue: queue}
	if queue == "" {
		out.Status = resourcecontext.KueuePositionNoLocalQueue
		return out
	}
	version, served := servedVersion(req.Discovery)
	if version == "" {
		served.LocalQueue = queue
		return &served
	}
	out.APIVersion = Group + "/" + version
	if req.Client == nil {
		return unavailable(out, readreason.ReadError, "Radar could not make this read as you, so it did not make it.")
	}
	namespace := req.Workload.GetNamespace()
	answer := r.fetch(ctx, req, version, namespace, queue)
	out.ReadAt = answer.readAt.UTC().Format(time.RFC3339)
	if answer.failure != nil {
		failure := answer.failure.Clone()
		failure.LocalQueue, failure.APIVersion, failure.ReadAt = queue, out.APIVersion, out.ReadAt
		return failure
	}
	rows := len(answer.rows)
	out.Rows, out.RowLimit = &rows, RowLimit
	if row, ok := locate(answer.rows, req.Workload); ok {
		lq, cq := row.PositionInLocalQueue, row.PositionInClusterQueue
		out.Status = resourcecontext.KueuePositionListed
		out.AheadInLocalQueue, out.AheadInClusterQueue = &lq, &cq
		return out
	}
	if rows >= RowLimit {
		out.Status = resourcecontext.KueuePositionLimitReached
	} else {
		out.Status = resourcecontext.KueuePositionNotListed
	}
	return out
}

func (r *Reader) fetch(ctx context.Context, req Request, version, namespace, queue string) fetched {
	if req.MemoKey == "" {
		return r.get(ctx, req, version, namespace, queue)
	}
	key := req.MemoKey + "\x00" + version + "\x00" + namespace + "\x00" + queue
	r.mu.Lock()
	if hit, ok := r.memo[key]; ok && r.now().Sub(hit.readAt) < r.ttl {
		r.mu.Unlock()
		return hit
	}
	r.mu.Unlock()
	// The shared read outlives one waiting caller's cancellation, but not
	// the reader's timeout; a caller with a shorter budget stops waiting.
	shared := context.WithoutCancel(ctx)
	results := r.inflight.DoChan(key, func() (any, error) {
		answer := r.get(shared, req, version, namespace, queue)
		r.mu.Lock()
		defer r.mu.Unlock()
		for k, entry := range r.memo {
			if r.now().Sub(entry.readAt) >= r.ttl {
				delete(r.memo, k)
			}
		}
		if answer.failure == nil {
			r.memo[key] = answer
		}
		return answer, nil
	})
	select {
	case result := <-results:
		return result.Val.(fetched)
	case <-ctx.Done():
		return fetched{failure: classify(ctx.Err(), req.Identity, namespace, queue), readAt: r.now()}
	}
}

func (r *Reader) get(ctx context.Context, req Request, version, namespace, queue string) fetched {
	path := fmt.Sprintf("/apis/%s/%s/namespaces/%s/localqueues/%s/pendingworkloads",
		Group, version, url.PathEscape(namespace), url.PathEscape(queue))
	readCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	readAt := r.now()
	failed := func(p *resourcecontext.KueuePendingPosition) fetched {
		return fetched{failure: p, readAt: readAt}
	}
	stream, err := req.Client.CoreV1().RESTClient().Get().AbsPath(path).
		Param("limit", fmt.Sprint(RowLimit)).Stream(readCtx)
	if err != nil {
		return failed(classify(err, req.Identity, namespace, queue))
	}
	defer stream.Close()
	body, err := io.ReadAll(io.LimitReader(stream, maxBodyBytes+1))
	if err != nil {
		return failed(classify(err, req.Identity, namespace, queue))
	}
	if len(body) > maxBodyBytes {
		return failed(unavailable(&resourcecontext.KueuePendingPosition{}, readreason.ReadError, "Kueue's answer was larger than Radar reads, so Radar did not use it."))
	}
	var summary pendingWorkloadsSummary
	if err := json.Unmarshal(body, &summary); err != nil || summary.Kind != "PendingWorkloadsSummary" {
		return failed(unavailable(&resourcecontext.KueuePendingPosition{}, readreason.ReadError, "Kueue's visibility API returned an answer Radar could not read."))
	}
	return fetched{rows: summary.Items, readAt: readAt}
}

// servedVersion picks the version to read, or returns the answer when there
// is nothing to read.
func servedVersion(d Discovery) (string, resourcecontext.KueuePendingPosition) {
	if d == nil {
		return "", resourcecontext.KueuePendingPosition{Status: resourcecontext.KueuePositionUnavailable, Code: readreason.ReadError, Message: "Radar's API discovery is not ready."}
	}
	catalog := d.Catalog()
	served := map[string]bool{}
	for _, resource := range catalog.Resources {
		if resource.Group == Group && resource.Name == "localqueues" {
			served[resource.Version] = true
		}
	}
	for _, version := range supportedVersions {
		if served[version] {
			return version, resourcecontext.KueuePendingPosition{}
		}
	}
	if len(served) > 0 {
		versions := make([]string, 0, len(served))
		for version := range served {
			versions = append(versions, version)
		}
		slices.Sort(versions)
		return "", resourcecontext.KueuePendingPosition{
			Status:  resourcecontext.KueuePositionUnsupported,
			Message: fmt.Sprintf("Kueue serves its visibility API at %v; Radar reads v1beta2 and v1beta1.", versions),
		}
	}
	if !catalog.Complete || d.LastRefreshFailed() || d.GroupHadPartialDiscovery(Group) {
		return "", resourcecontext.KueuePendingPosition{Status: resourcecontext.KueuePositionUnavailable, Code: readreason.ReadError, Message: "Radar's API discovery did not complete for Kueue's visibility API."}
	}
	return "", resourcecontext.KueuePendingPosition{Status: resourcecontext.KueuePositionNotServed}
}

func classify(err error, identity Identity, namespace, queue string) *resourcecontext.KueuePendingPosition {
	out := &resourcecontext.KueuePendingPosition{}
	var status *apierrors.StatusError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return unavailable(out, readreason.ReadError, "Kueue's visibility API did not answer in time.")
	case errors.Is(err, context.Canceled):
		return unavailable(out, readreason.ReadError, "The read was cancelled before Kueue answered.")
	case !errors.As(err, &status):
		return unavailable(out, readreason.ReadError, "Kueue's visibility API could not be reached.")
	}
	message := status.ErrStatus.Message
	need := fmt.Sprintf("Reading Kueue's pending order needs get on localqueues/pendingworkloads (%s) in namespace %s.", Group, namespace)
	switch {
	case apierrors.IsForbidden(err) && apierrors.ReasonForError(err) == metav1.StatusReason(pkgauth.UserTokenRequiredReason):
		return unavailable(out, readreason.ReadError, message)
	case apierrors.IsForbidden(err) && (identity == IdentityRadar || strings.Contains(message, "cannot impersonate")):
		// Radar's identity was refused, or may not act as the user at all.
		return unavailable(out, readreason.RadarForbidden, "Radar's Kubernetes identity isn't allowed this. "+need)
	case apierrors.IsForbidden(err) && identity == IdentitySystem && status.ErrStatus.Details != nil:
		// Radar's own check allowed the identity; the API server then
		// refused Radar's credentials.
		return unavailable(out, readreason.RadarForbidden, "Radar's Kubernetes identity isn't allowed this. "+need)
	case apierrors.IsForbidden(err):
		return unavailable(out, readreason.UserForbidden, "Your Kubernetes access doesn't allow this. "+need)
	case identity == IdentitySystem && k8s.IsPermissionCheckFailed(message):
		return unavailable(out, readreason.UserPermissionCheckFailed, message)
	case apierrors.IsUnauthorized(err) && identity == IdentityUser:
		return unavailable(out, readreason.ReadError, "Kubernetes rejected your credentials for this read.")
	case apierrors.IsUnauthorized(err):
		return unavailable(out, readreason.RadarCredentialsRejected, "Kubernetes rejected Radar's credentials for this read.")
	case apierrors.IsNotFound(err) && isLocalQueueNotFound(status):
		out.Status = resourcecontext.KueuePositionQueueUnknown
		out.Message = fmt.Sprintf("Kueue doesn't know LocalQueue %s in namespace %s.", queue, namespace)
		return out
	case apierrors.IsServiceUnavailable(err):
		return unavailable(out, readreason.ReadError, "Kueue's visibility server isn't available.")
	default:
		return unavailable(out, readreason.ReadError, fmt.Sprintf("Kueue's visibility API answered %d.", status.ErrStatus.Code))
	}
}

// isLocalQueueNotFound tells Kueue's own "no such LocalQueue" apart from a
// 404 for a path its API server does not serve.
func isLocalQueueNotFound(err *apierrors.StatusError) bool {
	details := err.ErrStatus.Details
	return details != nil && details.Group == Group && details.Kind == "localqueue"
}

func unavailable(out *resourcecontext.KueuePendingPosition, code readreason.Code, message string) *resourcecontext.KueuePendingPosition {
	out.Status, out.Code, out.Message = resourcecontext.KueuePositionUnavailable, code, message
	return out
}

type pendingWorkloadsSummary struct {
	Kind  string            `json:"kind"`
	Items []pendingWorkload `json:"items"`
}

type rowOwner struct {
	UID types.UID `json:"uid"`
}

type pendingWorkload struct {
	Metadata struct {
		Name              string      `json:"name"`
		Namespace         string      `json:"namespace"`
		CreationTimestamp metav1.Time `json:"creationTimestamp"`
		OwnerReferences   []rowOwner  `json:"ownerReferences"`
	} `json:"metadata"`
	PositionInClusterQueue int32 `json:"positionInClusterQueue"`
	PositionInLocalQueue   int32 `json:"positionInLocalQueue"`
}

// locate finds the row for this Workload. The API names rows without a UID,
// so a row is this Workload when its namespace, name and creation time match
// and, if the Workload has a controller owner, a row owner has that owner's
// UID. A same-named Workload created at another time is a different object.
func locate(rows []pendingWorkload, workload *unstructured.Unstructured) (pendingWorkload, bool) {
	created := workload.GetCreationTimestamp()
	owner := metav1.GetControllerOf(workload)
	for _, row := range rows {
		if row.Metadata.Namespace != workload.GetNamespace() || row.Metadata.Name != workload.GetName() {
			continue
		}
		if row.Metadata.CreationTimestamp.Unix() != created.Unix() {
			continue
		}
		if owner != nil && !slices.ContainsFunc(row.Metadata.OwnerReferences, func(ref rowOwner) bool { return ref.UID == owner.UID }) {
			continue
		}
		return row, true
	}
	return pendingWorkload{}, false
}
