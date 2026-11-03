package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"golang.org/x/sync/errgroup"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/kueuequeues"
	"github.com/skyhook-io/radar/internal/readplan"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
	"github.com/skyhook-io/radar/pkg/schedulinginsight"
)

const (
	kueueQueueWorkloadsDefaultLimit = 50
	kueueQueueWorkloadsMaxLimit     = 200
	// kueueQueueLocalQueuesLimit bounds the LocalQueues a ClusterQueue
	// response lists; total still counts them all.
	kueueQueueLocalQueuesLimit = 500
	kueueQueueCursorVersion    = 1
	maxKueueQueueCursorLength  = 2048
)

var kueueQueueSyncWait = 3 * time.Second

// KueueQueueWorkloadsResponse answers which Workloads belong to one queue.
// Each part is either what Radar read for the caller or why it couldn't:
// a denied Workload list still carries the LocalQueues and the counts their
// controller reports.
type KueueQueueWorkloadsResponse struct {
	Queue            KueueQueueIdentity     `json:"queue"`
	Workloads        *KueueQueueMembers     `json:"workloads,omitempty"`
	WorkloadsError   *resourceapi.ReadError `json:"workloadsError,omitempty"`
	LocalQueues      *KueueQueueLocalQueues `json:"localQueues,omitempty"`
	LocalQueuesError *resourceapi.ReadError `json:"localQueuesError,omitempty"`
}

type KueueQueueIdentity struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
	// ClusterQueue is a LocalQueue's spec.clusterQueue, as it is now.
	ClusterQueue string `json:"clusterQueue,omitempty"`
}

// KueueQueueMembers is one page of the queue's Workloads. Matching and Counts
// describe every member Radar read for the caller, not just this page, as of
// this page's read: a later page may report other numbers.
type KueueQueueMembers struct {
	Items []KueueQueueWorkload `json:"items"`
	// Matching is the members the request's membership filter keeps.
	Matching int                `json:"matching"`
	Counts   kueuequeues.Counts `json:"counts"`
	// Next continues the list after this page; absent on the last page.
	Next     string               `json:"next,omitempty"`
	Coverage resourceapi.Coverage `json:"coverage"`
	// SubmissionUnresolved (ClusterQueue only) is where Radar read Workloads
	// but not LocalQueues: Workloads there are listed only when their
	// reservation names the queue, since where their LocalQueue points is
	// unknown.
	SubmissionUnresolved *resourceapi.NameSet `json:"submissionUnresolved,omitempty"`
}

// KueueQueueWorkload is one member Workload.
type KueueQueueWorkload struct {
	Namespace  string      `json:"namespace"`
	Name       string      `json:"name"`
	UID        string      `json:"uid"`
	APIVersion string      `json:"apiVersion"`
	CreatedAt  metav1.Time `json:"createdAt"`
	Deleting   bool        `json:"deleting"`
	// Phase is Kueue's lifecycle as Radar projects it for v1beta2 Workloads;
	// absent for other versions.
	Phase resourcecontext.KueuePhase `json:"phase,omitempty"`
	// PhaseStale: the condition behind Phase describes an older generation.
	PhaseStale bool               `json:"phaseStale,omitempty"`
	Membership []kueuequeues.Kind `json:"membership"`
	QueueName  string             `json:"queueName,omitempty"`
	// Submission is where the Workload's LocalQueue points now, when Radar
	// read it.
	Submission *KueueQueueSubmission `json:"submission,omitempty"`
	// Reservation is the ClusterQueue the Workload reports its admission in.
	Reservation *KueueQueueReservation `json:"reservation,omitempty"`
}

type KueueQueueSubmission struct {
	LocalQueue   string `json:"localQueue"`
	ClusterQueue string `json:"clusterQueue"`
}

type KueueQueueReservation struct {
	ClusterQueue string `json:"clusterQueue"`
}

// KueueQueueLocalQueues are the LocalQueues pointing at a ClusterQueue.
type KueueQueueLocalQueues struct {
	Items []KueueQueueLocalQueue `json:"items"`
	// Total counts every LocalQueue found; Items lists up to 500.
	Total    int                  `json:"total"`
	Coverage resourceapi.Coverage `json:"coverage"`
}

type KueueQueueLocalQueue struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	// Reported are the LocalQueue's own counts, as its controller reports them.
	Reported KueueQueueReportedCounts `json:"reported"`
	// WorkloadsRead: Radar read Workloads in the LocalQueue's namespace for
	// the caller. When it didn't, WorkloadsGap says why, if Radar knows.
	WorkloadsRead bool            `json:"workloadsRead"`
	WorkloadsGap  readreason.Code `json:"workloadsGap,omitempty"`
}

// KueueQueueReportedCounts are null when the controller hasn't reported them.
type KueueQueueReportedCounts struct {
	Pending   *int64 `json:"pending"`
	Reserving *int64 `json:"reserving"`
	Admitted  *int64 `json:"admitted"`
}

// kueueQueueHandles are the cluster connection a request reads through, from
// authorization to answer.
type kueueQueueHandles struct {
	cache     *k8s.ResourceCache
	dynamic   *k8s.DynamicResourceCache
	discovery *k8s.ResourceDiscovery
}

func captureKueueQueueHandles() (kueueQueueHandles, bool) {
	h := kueueQueueHandles{cache: k8s.GetResourceCache(), dynamic: k8s.GetDynamicResourceCache(), discovery: k8s.GetResourceDiscovery()}
	return h, h.cache != nil && h.dynamic != nil && h.discovery != nil && h.current()
}

func (h kueueQueueHandles) current() bool {
	return h.cache == k8s.GetResourceCache() && h.dynamic == k8s.GetDynamicResourceCache() && !h.dynamic.IsStopped() && h.discovery == k8s.GetResourceDiscovery()
}

// kueueQueuePage is the paging part of a request.
type kueueQueuePage struct {
	limit      int
	membership kueuequeues.Kind
	after      string
	scope      string
}

type kueueQueueCursor struct {
	Version    int    `json:"v"`
	Scope      string `json:"s"`
	Membership string `json:"m"`
	Context    string `json:"c"`
	After      string `json:"a"`
}

var errKueueQueueCursor = errors.New("the cursor does not belong to this list; restart from the first page")

// parseKueueQueuePage reads limit, membership and cursor. A cursor carries
// the queue (with its UID, so a re-created queue starts over), the filter
// and the cluster context it was issued for, and is refused for any other.
func parseKueueQueuePage(r *http.Request, scope string, clusterQueue bool) (kueueQueuePage, error) {
	q := r.URL.Query()
	page := kueueQueuePage{limit: kueueQueueWorkloadsDefaultLimit, scope: scope}
	for _, key := range []string{"limit", "membership", "cursor"} {
		if len(q[key]) > 1 {
			return page, fmt.Errorf("%s must be given at most once", key)
		}
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > kueueQueueWorkloadsMaxLimit {
			return page, fmt.Errorf("limit must be between 1 and %d", kueueQueueWorkloadsMaxLimit)
		}
		page.limit = n
	}
	switch m := kueuequeues.Kind(q.Get("membership")); m {
	case "":
	case kueuequeues.Submission, kueuequeues.Reservation:
		if !clusterQueue {
			return page, fmt.Errorf("membership filters apply to ClusterQueues only")
		}
		page.membership = m
	default:
		return page, fmt.Errorf("membership must be %q or %q", kueuequeues.Submission, kueuequeues.Reservation)
	}
	encoded := q.Get("cursor")
	if encoded == "" {
		return page, nil
	}
	if len(encoded) > maxKueueQueueCursorLength {
		return page, errKueueQueueCursor
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return page, errKueueQueueCursor
	}
	var cursor kueueQueueCursor
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil || decoder.More() {
		return page, errKueueQueueCursor
	}
	if cursor.Version != kueueQueueCursorVersion || cursor.Scope != scope || cursor.Membership != string(page.membership) || cursor.Context != k8s.ActiveClusterContext() || cursor.After == "" {
		return page, errKueueQueueCursor
	}
	page.after = cursor.After
	return page, nil
}

func (p kueueQueuePage) cursor(after string) string {
	raw, _ := json.Marshal(kueueQueueCursor{Version: kueueQueueCursorVersion, Scope: p.scope, Membership: string(p.membership), Context: k8s.ActiveClusterContext(), After: after})
	return base64.RawURLEncoding.EncodeToString(raw)
}

// handleKueueClusterQueueWorkloads lists the Workloads that belong to a
// ClusterQueue, by submission or reservation, and the LocalQueues pointing
// at it, within the namespaces the caller can see. The caller needs get on
// the ClusterQueue; Workloads and LocalQueues are each read only where the
// caller may list that kind, namespace by namespace — not inferred from Pod
// access — and what wasn't read is a coverage gap, never an empty answer.
func (s *Server) handleKueueClusterQueueWorkloads(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	h, ok := captureKueueQueueHandles()
	if !ok {
		s.writeCollectionFailure(w, "clusterqueues", k8s.ErrDynamicNotReady)
		return
	}
	name := chi.URLParam(r, "name")
	if allowed, checked := s.canReadDecision(r, kueueGroup, "clusterqueues", "", "get"); !allowed {
		s.writeCollectionFailure(w, "clusterqueues", permissionFailure(checked, "no access to this ClusterQueue"))
		return
	}
	queue, err := readDynamicResourceGet(r.Context(), h.cache, "ClusterQueue", kueueGroup, "", name)
	if err != nil {
		s.writeKueueQueueGetError(w, "ClusterQueue", name, err)
		return
	}
	identity := KueueQueueIdentity{Kind: "ClusterQueue", Name: name, UID: string(queue.GetUID())}
	page, err := parseKueueQueuePage(r, "ClusterQueue/"+name+"/"+identity.UID, true)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	response := KueueQueueWorkloadsResponse{Queue: identity}

	target, code := s.namespaceListTarget(r, h.cache, nil)
	localQueues, lqFailure := s.readKueueIndexed(r, h, target, code, "LocalQueue", "localqueues", []string{k8score.KueueLocalQueueTargetKey(name)})
	keys := []string{k8score.KueueReservationKey(name)}
	targets := map[string]string{}
	if lqFailure != nil {
		body, _ := readErrorBody("localqueues", lqFailure.err, &lqFailure.target)
		response.LocalQueuesError = &body
	} else {
		for _, lq := range localQueues.objects {
			if kueuequeues.LocalQueueTarget(lq) != name {
				continue
			}
			targets[lq.GetNamespace()+"/"+lq.GetName()] = name
			keys = append(keys, k8score.KueueSubmissionKey(lq.GetNamespace(), lq.GetName()))
		}
	}
	workloads, wlFailure := s.readKueueIndexed(r, h, target, code, "Workload", "workloads", keys)
	if wlFailure != nil {
		body, _ := readErrorBody("workloads", wlFailure.err, &wlFailure.target)
		response.WorkloadsError = &body
	} else {
		read := kueueLocalQueueTargets{targets: targets, read: localQueues.covers, lookup: func(namespace, name string) string {
			if lq, found, ok := h.dynamic.ReadableObject(localQueues.gvr, namespace, name); found && ok {
				return kueuequeues.LocalQueueTarget(lq)
			}
			return ""
		}}
		if lqFailure != nil {
			read.read = func(string) bool { return false }
		}
		members := kueuequeues.ClusterQueueMembers(name, workloads.objects, read)
		response.Workloads = kueueQueueMembersPage(members, page, workloads.coverage)
		response.Workloads.SubmissionUnresolved = submissionUnresolved(workloads, localQueues, lqFailure != nil)
	}
	if lqFailure == nil {
		response.LocalQueues = kueueQueueLocalQueueList(name, localQueues, workloads, wlFailure)
	}
	if !h.current() {
		s.writeCollectionFailure(w, "workloads", errReadHandlesChanged)
		return
	}
	s.writeJSON(w, response)
}

// handleKueueLocalQueueWorkloads lists the Workloads submitted to a
// LocalQueue. The caller needs to see the LocalQueue's namespace and get the
// LocalQueue; Workloads are read only if the caller may list them there.
func (s *Server) handleKueueLocalQueueWorkloads(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	h, ok := captureKueueQueueHandles()
	if !ok {
		s.writeCollectionFailure(w, "localqueues", k8s.ErrDynamicNotReady)
		return
	}
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	if failure := s.preflightResourceGetFrom(r, h.cache, "localqueues", namespace, name, kueueGroup); failure != nil {
		s.writeCollectionFailure(w, "localqueues", failure)
		return
	}
	if failure := outsideNamespacePin(namespace); failure != nil {
		s.writeCollectionFailure(w, "localqueues", failure)
		return
	}
	if allowed, checked := s.canReadDecision(r, kueueGroup, "localqueues", namespace, "get"); !allowed {
		s.writeCollectionFailure(w, "localqueues", permissionFailure(checked, "no access to this LocalQueue"))
		return
	}
	queue, err := readDynamicResourceGet(r.Context(), h.cache, "LocalQueue", kueueGroup, namespace, name)
	if err != nil {
		s.writeKueueQueueGetError(w, "LocalQueue", namespace+"/"+name, err)
		return
	}
	clusterQueue := kueuequeues.LocalQueueTarget(queue)
	identity := KueueQueueIdentity{Kind: "LocalQueue", Namespace: namespace, Name: name, UID: string(queue.GetUID()), ClusterQueue: clusterQueue}
	page, err := parseKueueQueuePage(r, "LocalQueue/"+namespace+"/"+name+"/"+identity.UID, false)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	response := KueueQueueWorkloadsResponse{Queue: identity}
	target, code := s.namespaceListTarget(r, h.cache, []string{namespace})
	workloads, failure := s.readKueueIndexed(r, h, target, code, "Workload", "workloads", []string{k8score.KueueSubmissionKey(namespace, name)})
	if failure != nil {
		body, _ := readErrorBody("workloads", failure.err, &failure.target)
		response.WorkloadsError = &body
	} else {
		members := kueuequeues.LocalQueueMembers(namespace, name, clusterQueue, workloads.objects)
		response.Workloads = kueueQueueMembersPage(members, page, workloads.coverage)
	}
	if !h.current() {
		s.writeCollectionFailure(w, "workloads", errReadHandlesChanged)
		return
	}
	s.writeJSON(w, response)
}

func (s *Server) writeKueueQueueGetError(w http.ResponseWriter, kind, name string, err error) {
	var failure *resourceReadFailure
	switch {
	case errors.As(err, &failure):
	case errors.Is(err, k8score.ErrResourceNotFound) || apierrors.IsNotFound(err):
		failure = readFailuref(readreason.ObjectNotFound, "%s %s not found", kind, name)
	default:
		failure = collectionFailure(strings.ToLower(kind)+"s", err)
	}
	s.writeCollectionFailure(w, strings.ToLower(kind)+"s", failure)
}

// kueueLocalQueueTargets answers kueuequeues.LocalQueues from the
// LocalQueues pointing at the ClusterQueue: in a namespace whose LocalQueues
// were read, any other LocalQueue points elsewhere or doesn't exist.
// A LocalQueue that points elsewhere is looked up by name, so a member by
// reservation alone can say where its LocalQueue points now.
type kueueLocalQueueTargets struct {
	targets map[string]string
	read    func(namespace string) bool
	lookup  func(namespace, name string) string
}

func (l kueueLocalQueueTargets) Target(namespace, name string) (string, bool) {
	if !l.read(namespace) {
		return "", false
	}
	if target, ok := l.targets[namespace+"/"+name]; ok {
		return target, true
	}
	return l.lookup(namespace, name), true
}

func kueueQueueMembersPage(members []kueuequeues.Member, page kueueQueuePage, coverage resourceapi.Coverage) *KueueQueueMembers {
	matching := kueuequeues.Filter(members, page.membership)
	rows, more := kueuequeues.Page(matching, page.after, page.limit)
	out := &KueueQueueMembers{Items: make([]KueueQueueWorkload, 0, len(rows)), Matching: len(matching), Counts: kueuequeues.Count(members), Coverage: coverage}
	for _, m := range rows {
		out.Items = append(out.Items, kueueQueueWorkload(m))
	}
	if more && len(rows) > 0 {
		out.Next = page.cursor(kueuequeues.Key(rows[len(rows)-1]))
	}
	return out
}

func kueueQueueWorkload(m kueuequeues.Member) KueueQueueWorkload {
	w := m.Workload
	row := KueueQueueWorkload{
		Namespace: w.GetNamespace(), Name: w.GetName(), UID: string(w.GetUID()), APIVersion: w.GetAPIVersion(),
		CreatedAt: w.GetCreationTimestamp(), Deleting: w.GetDeletionTimestamp() != nil,
		Membership: m.Kinds, QueueName: m.QueueName,
	}
	if m.QueueName != "" && m.SubmittedTo != "" {
		row.Submission = &KueueQueueSubmission{LocalQueue: m.QueueName, ClusterQueue: m.SubmittedTo}
	}
	if m.ReservedIn != "" {
		row.Reservation = &KueueQueueReservation{ClusterQueue: m.ReservedIn}
	}
	if summary := schedulinginsight.ForResource(w, resourcecontext.TierBasic); summary != nil && len(summary.Observations) > 0 && summary.Observations[0].Kueue != nil {
		obs := summary.Observations[0]
		row.Phase = obs.Kueue.Phase
		if c := obs.PrimaryCondition; c != nil && c.ObservedGeneration > 0 && c.ObservedGeneration < obs.SubjectGeneration {
			row.PhaseStale = true
		}
	}
	return row
}

// submissionUnresolved is where Workloads were read but LocalQueues weren't.
func submissionUnresolved(workloads, localQueues kueueKindRead, localQueuesFailed bool) *resourceapi.NameSet {
	if localQueuesFailed {
		if workloads.all {
			return resourceapi.AllNames()
		}
		return resourceapi.Names(workloads.namespaces(), resourceapi.RESTSetLimit)
	}
	if localQueues.all {
		return nil
	}
	if workloads.all {
		return resourceapi.UnknownNames()
	}
	var unresolved []string
	for _, ns := range workloads.namespaces() {
		if !localQueues.covers(ns) {
			unresolved = append(unresolved, ns)
		}
	}
	if len(unresolved) == 0 {
		return nil
	}
	return resourceapi.Names(unresolved, resourceapi.RESTSetLimit)
}

func kueueQueueLocalQueueList(clusterQueue string, localQueues, workloads kueueKindRead, workloadsFailure *kueueReadFailure) *KueueQueueLocalQueues {
	var items []KueueQueueLocalQueue
	for _, lq := range localQueues.objects {
		if kueuequeues.LocalQueueTarget(lq) != clusterQueue {
			continue
		}
		item := KueueQueueLocalQueue{Namespace: lq.GetNamespace(), Name: lq.GetName(), Reported: KueueQueueReportedCounts{
			Pending: reportedCount(lq, "pendingWorkloads"), Reserving: reportedCount(lq, "reservingWorkloads"), Admitted: reportedCount(lq, "admittedWorkloads"),
		}}
		switch {
		case workloadsFailure != nil:
			item.WorkloadsGap = workloadsFailure.code(lq.GetNamespace())
		case workloads.covers(lq.GetNamespace()):
			item.WorkloadsRead = true
		default:
			item.WorkloadsGap = workloads.gapCode(lq.GetNamespace())
		}
		items = append(items, item)
	}
	slices.SortFunc(items, func(a, b KueueQueueLocalQueue) int {
		return strings.Compare(a.Namespace+"/"+a.Name, b.Namespace+"/"+b.Name)
	})
	out := &KueueQueueLocalQueues{Items: items, Total: len(items), Coverage: localQueues.coverage}
	if out.Items == nil {
		out.Items = []KueueQueueLocalQueue{}
	}
	if len(out.Items) > kueueQueueLocalQueuesLimit {
		out.Items = out.Items[:kueueQueueLocalQueuesLimit]
	}
	return out
}

func reportedCount(lq *unstructured.Unstructured, field string) *int64 {
	value, found, err := unstructured.NestedInt64(lq.Object, "status", field)
	if !found || err != nil {
		return nil
	}
	return &value
}

// kueueKindRead is what Radar read of one Kueue kind for the caller: the
// objects filed under the requested index keys in the namespaces read, and
// the coverage of the read.
type kueueKindRead struct {
	gvr     schema.GroupVersionResource
	objects []*unstructured.Unstructured
	// all: every namespace of the target was read; otherwise read lists them.
	all      bool
	read     map[string]bool
	coverage resourceapi.Coverage
}

func (k kueueKindRead) covers(namespace string) bool { return k.all || k.read[namespace] }

func (k kueueKindRead) namespaces() []string {
	out := make([]string, 0, len(k.read))
	for ns := range k.read {
		out = append(out, ns)
	}
	slices.Sort(out)
	return out
}

// gapCode is the code of the gap that names namespace, or "" when no gap
// names it (it is outside the scope, or in a gap Radar can't list).
func (k kueueKindRead) gapCode(namespace string) readreason.Code {
	for _, gap := range k.coverage.Gaps {
		if gap.Namespaces != nil && slices.Contains(gap.Namespaces.Names, namespace) {
			return gap.Code
		}
	}
	return ""
}

// kueueReadFailure is a read that returned nothing, with the target its
// coverage describes.
type kueueReadFailure struct {
	err    *resourceReadFailure
	target listTarget
}

func (f *kueueReadFailure) code(namespace string) readreason.Code {
	for _, gap := range f.err.gaps {
		if gap.Namespaces != nil && slices.Contains(gap.Namespaces.Names, namespace) {
			return gap.Code
		}
	}
	for _, gap := range f.target.gaps {
		if gap.Namespaces != nil && slices.Contains(gap.Namespaces.Names, namespace) {
			return gap.Code
		}
	}
	return f.err.code
}

// readKueueIndexed reads the objects of a namespaced Kueue kind filed under
// keys in KueueQueueIndex, within target, for the caller. The caller's own
// list permission is checked namespace by namespace; Radar's collection
// decides the rest through the same planner REST lists use. Only the indexed
// objects are read, from the cache itself: the cost follows the queue's
// size, not the cluster's.
func (s *Server) readKueueIndexed(r *http.Request, h kueueQueueHandles, target listTarget, selectionCode readreason.Code, kind, resource string, keys []string) (kueueKindRead, *kueueReadFailure) {
	fail := func(t listTarget, err *resourceReadFailure) (kueueKindRead, *kueueReadFailure) {
		return kueueKindRead{}, &kueueReadFailure{err: err, target: t}
	}
	if selectionCode == readreason.UserPermissionCheckFailed {
		return fail(target, readFailuref(selectionCode, "Radar could not determine your namespace access; retry when the permission check is available"))
	}
	authorized, target, failure := s.kueueListAccess(r, h.cache, target, resource)
	if failure != nil {
		return fail(target, failure)
	}
	gvr, found := h.discovery.GetGVRWithGroup(kind, kueueGroup)
	if !found {
		if h.discovery.GroupHadPartialDiscovery(kueueGroup) {
			return fail(target, dynamicPlanFailure(resource, readreason.KindSyncPending))
		}
		return fail(target, readFailuref(readreason.KindUnknown, "%s are not served by this cluster", resource))
	}
	warmKueueKind(r.Context(), h.dynamic, gvr, authorized)

	inventory := readplan.ForInventory(readplan.Dynamic(h.dynamic, gvr, authorized, false), authorized, target.explicit, false)
	gaps := inventoryGaps(inventory)
	if inventory.Collection != nil {
		target = target.narrowedByCollection(inventory.Collection.Namespaces, inventory.Collection.Outside)
	}
	if !inventory.CanReadTarget {
		codes := make([]readreason.Code, 0, len(gaps))
		for _, gap := range gaps {
			codes = append(codes, gap.Code)
		}
		failure := dynamicPlanFailure(resource, readreason.Primary(codes))
		failure.gaps = gaps
		return fail(target, failure)
	}

	out := kueueKindRead{gvr: gvr, all: inventory.All}
	if inventory.All {
		objs, ok := h.dynamic.ReadableIndexed(gvr, "", k8score.KueueQueueIndex, keys...)
		if !ok {
			failure := dynamicPlanFailure(resource, readreason.KindSyncPending)
			failure.gaps = append(gaps, resourceapi.Gap{Code: readreason.KindSyncPending})
			return fail(target, failure)
		}
		out.objects = objs
	} else {
		out.read = make(map[string]bool, len(inventory.Namespaces))
		var pending []string
		wanted := make(map[string]bool, len(inventory.Namespaces))
		for _, ns := range inventory.Namespaces {
			wanted[ns] = true
		}
		// A cluster-wide store serves every namespace: read it once.
		if objs, ok := h.dynamic.ReadableIndexed(gvr, "", k8score.KueueQueueIndex, keys...); ok {
			for _, u := range objs {
				if wanted[u.GetNamespace()] {
					out.objects = append(out.objects, u)
				}
			}
			for ns := range wanted {
				out.read[ns] = true
			}
		} else {
			for _, ns := range inventory.Namespaces {
				objs, ok := h.dynamic.ReadableIndexed(gvr, ns, k8score.KueueQueueIndex, keys...)
				if !ok {
					pending = append(pending, ns)
					continue
				}
				out.read[ns] = true
				out.objects = append(out.objects, objs...)
			}
		}
		if len(pending) > 0 {
			gaps = append(gaps, gapOf(readreason.KindSyncPending, pending))
		}
		if len(out.read) == 0 {
			failure := dynamicPlanFailure(resource, readreason.KindSyncPending)
			failure.gaps = gaps
			return fail(target, failure)
		}
	}
	var read []string
	if !out.all {
		read = out.namespaces()
	}
	out.coverage = target.coverage(read, gaps)
	return out, nil
}

// kueueListAccess narrows target to where the caller may list resource: nil
// for every namespace. A namespace the caller can see but not list it in is a
// user_forbidden gap; one whose check failed, user_permission_check_failed.
func (s *Server) kueueListAccess(r *http.Request, cache *k8s.ResourceCache, target listTarget, resource string) ([]string, listTarget, *resourceReadFailure) {
	candidates := target.namespaces
	if candidates == nil {
		allowed, checked := s.canReadDecision(r, kueueGroup, resource, "", "list")
		if allowed {
			return nil, target, nil
		}
		if !checked {
			return nil, target, permissionFailure(false, "")
		}
		// The caller sees every namespace but may not list resource across
		// all of them: check each namespace Radar knows of.
		candidates = namespaceNamesInCache(cache)
	}
	if len(candidates) == 0 {
		return nil, target, readFailuref(readreason.UserNoNamespaceAccess, "no namespace access")
	}
	allowed, denied, unchecked := s.partitionNamespacesByCanRead(r, kueueGroup, resource, "list", candidates)
	var gaps []resourceapi.Gap
	if len(denied) > 0 {
		gaps = append(gaps, gapOf(readreason.UserForbidden, denied))
	}
	if len(unchecked) > 0 {
		gaps = append(gaps, gapOf(readreason.UserPermissionCheckFailed, unchecked))
	}
	target = target.withGaps(gaps)
	if len(allowed) == 0 {
		return nil, target, permissionFailure(len(unchecked) == 0, fmt.Sprintf("insufficient permissions to list %s", resource))
	}
	return allowed, target, nil
}

// warmKueueKind starts the watches a read of namespaces (nil: every
// namespace) needs and waits briefly for them to load. What doesn't load in
// time is reported by the planner as still loading.
func warmKueueKind(ctx context.Context, dc *k8s.DynamicResourceCache, gvr schema.GroupVersionResource, namespaces []string) {
	ctx, cancel := context.WithTimeout(ctx, kueueQueueSyncWait)
	defer cancel()
	scopes := namespaces
	if scopes == nil {
		scopes = []string{""}
	}
	var g errgroup.Group
	g.SetLimit(8)
	for _, ns := range scopes {
		g.Go(func() error {
			if err := dc.EnsureWatchingContext(ctx, gvr, ns); err == nil {
				dc.WaitForSyncContext(ctx, gvr, ns)
			}
			return nil
		})
	}
	_ = g.Wait()
}
