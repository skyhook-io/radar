package server

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/version"
	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
)

// The Cluster report bundle mirrors `kubectl cnpg report cluster`: the
// Cluster, its Pods, Jobs, PVCs and events as manifests, optionally the Pods'
// logs. Radar adds what it already knows (Backups, ScheduledBackups, Poolers,
// the ObjectStore, operator and plugin versions, the runtime and storage
// snapshots) and a coverage record of everything it could not read and why.
// Secret values are never read; the bundle lists the Secrets the cluster
// references by name. Logs, and the query text inside them, are opt-in.

const (
	cnpgReportTotalCap      = 32 << 20
	cnpgReportLogCap        = 2 << 20
	cnpgReportDefaultTail   = 1000
	cnpgReportMaxTail       = 10000
	cnpgReportTimeout       = 40 * time.Second
	cnpgReportRedacted      = "[REDACTED]"
	cnpgReportQueryRedacted = "[query text omitted: download with query text included to keep it]"
)

var (
	errCNPGReportFull        = errors.New("report size bound reached")
	cnpgObjectStoreGVR       = schema.GroupVersionResource{Group: cnpgBarmanGroup, Version: "v1", Resource: "objectstores"}
	cnpgGrantListBackups     = Grant{Verb: "list", Group: cnpgGroup, Resource: "backups"}
	cnpgGrantListSchedules   = Grant{Verb: "list", Group: cnpgGroup, Resource: "scheduledbackups"}
	cnpgGrantListPoolers     = Grant{Verb: "list", Group: cnpgGroup, Resource: "poolers"}
	cnpgGrantGetObjectStores = Grant{Verb: "get", Group: cnpgBarmanGroup, Resource: "objectstores"}
	cnpgGrantGetPodLogs      = Grant{Verb: "get", Resource: "pods", Subresource: "log"}
)

type cnpgReportOptions struct {
	Logs      bool  `json:"logs"`
	QueryText bool  `json:"queryText"`
	TailLines int64 `json:"tailLines,omitempty"`
}

// CNPGReportItem is one entry of the bundle's contents: a file that was
// written, or a read that was skipped and why.
type CNPGReportItem struct {
	Item  string `json:"item"`
	File  string `json:"file,omitempty"`
	Count *int   `json:"count,omitempty"`
	ReadSource
	Note string `json:"note,omitempty"`
}

type CNPGReportSecretRef struct {
	Name         string   `json:"name"`
	ReferencedBy []string `json:"referencedBy"`
}

// CNPGReportIndex is report.json at the root of the bundle.
type CNPGReportIndex struct {
	GeneratedAt  string                `json:"generatedAt"`
	RadarVersion string                `json:"radarVersion"`
	Context      string                `json:"context"`
	RequestedBy  string                `json:"requestedBy,omitempty"`
	Cluster      CNPGRuntimeObjectRef  `json:"cluster"`
	Options      cnpgReportOptions     `json:"options"`
	Contents     []CNPGReportItem      `json:"contents"`
	Secrets      []CNPGReportSecretRef `json:"secrets"`
	BoundBytes   int64                 `json:"boundBytes"`
	Truncated    bool                  `json:"truncated"`
}

type cnpgReportZip struct {
	zw    *zip.Writer
	root  string
	used  int64
	limit int64
}

func (z *cnpgReportZip) add(path string, data []byte) error {
	if z.used+int64(len(data)) > z.limit {
		return errCNPGReportFull
	}
	f, err := z.zw.CreateHeader(&zip.FileHeader{Name: z.root + "/" + path, Method: zip.Deflate, Modified: time.Now()})
	if err != nil {
		return err
	}
	z.used += int64(len(data))
	_, err = f.Write(data)
	return err
}

func parseCNPGReportOptions(r *http.Request) (cnpgReportOptions, error) {
	q := r.URL.Query()
	opts := cnpgReportOptions{Logs: q.Get("logs") == "true", QueryText: q.Get("queryText") == "true", TailLines: cnpgReportDefaultTail}
	if v := q.Get("tailLines"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 || n > cnpgReportMaxTail {
			return opts, fmt.Errorf("tailLines must be between 1 and %d", cnpgReportMaxTail)
		}
		opts.TailLines = n
	}
	if opts.QueryText && !opts.Logs {
		return opts, errors.New("queryText applies to logs; set logs=true as well")
	}
	if !opts.Logs {
		opts.TailLines = 0
	}
	return opts, nil
}

// handleCNPGClusterReport serves GET /api/cnpg/clusters/{ns}/{name}/report as a
// zip download. Only the Cluster read itself can fail the request; every other
// read is skipped and recorded in report.json.
func (s *Server) handleCNPGClusterReport(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	opts, err := parseCNPGReportOptions(r)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	dyn, contextName := s.getDynamicClientSnapshotForRequest(r)
	typed := s.getClientForRequest(r)
	if dyn == nil || typed == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), cnpgReportTimeout)
	defer cancel()
	cluster, err := dyn.Resource(cnpgClusterGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		s.writeCNPGReadError(w, err, cnpgGrantGetCluster, namespace, name)
		return
	}
	auth.AuditLog(r, namespace, name)

	now := time.Now().UTC()
	root := fmt.Sprintf("report_cluster_%s_%s", name, now.Format(cnpgCompactStamp))
	var buf bytes.Buffer
	z := &cnpgReportZip{zw: zip.NewWriter(&buf), root: root, limit: cnpgReportTotalCap}
	index := CNPGReportIndex{
		GeneratedAt:  now.Format(time.RFC3339),
		RadarVersion: version.Current,
		Context:      contextName,
		Cluster:      CNPGRuntimeObjectRef{Namespace: namespace, Name: name, UID: cluster.GetUID()},
		Options:      opts,
		BoundBytes:   cnpgReportTotalCap,
	}
	if user := auth.UserFromContext(r.Context()); user != nil {
		index.RequestedBy = user.Username
	}
	b := &cnpgReportBuilder{s: s, r: r.WithContext(ctx), ctx: ctx, dyn: dyn, typed: typed, cluster: cluster, z: z, index: &index, secrets: map[string]map[string]bool{}}
	b.build(opts)

	index.Secrets = b.secretRefs()
	data, _ := json.MarshalIndent(index, "", "  ")
	z.limit += int64(len(data))
	if err := z.add("report.json", data); err != nil {
		log.Printf("[cnpg] Failed to write report index for %s/%s: %v", sanitizeForLog(namespace), sanitizeForLog(name), err)
		s.writeError(w, http.StatusInternalServerError, "failed to build the report")
		return
	}
	if err := z.zw.Close(); err != nil {
		log.Printf("[cnpg] Failed to close report zip for %s/%s: %v", sanitizeForLog(namespace), sanitizeForLog(name), err)
		s.writeError(w, http.StatusInternalServerError, "failed to build the report")
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.zip"`, root))
	w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(buf.Bytes()); err != nil {
		log.Printf("[cnpg] Failed to send report for %s/%s: %v", sanitizeForLog(namespace), sanitizeForLog(name), err)
	}
}

type cnpgReportBuilder struct {
	s       *Server
	r       *http.Request
	ctx     context.Context
	dyn     dynamic.Interface
	typed   kubernetes.Interface
	cluster *unstructured.Unstructured
	z       *cnpgReportZip
	index   *CNPGReportIndex
	secrets map[string]map[string]bool
}

func (b *cnpgReportBuilder) record(item CNPGReportItem) {
	b.index.Contents = append(b.index.Contents, item)
}

func (b *cnpgReportBuilder) write(item CNPGReportItem, file string, v any, count int) {
	data, err := yaml.Marshal(v)
	if err != nil {
		item.ReadSource = ReadSource{State: cnpgReadError, Reason: err.Error()}
		b.record(item)
		return
	}
	b.writeBytes(item, file, data, &count)
}

func (b *cnpgReportBuilder) writeBytes(item CNPGReportItem, file string, data []byte, count *int) {
	if err := b.z.add(file, data); err != nil {
		b.index.Truncated = b.index.Truncated || errors.Is(err, errCNPGReportFull)
		item.ReadSource = ReadSource{State: cnpgReadSkipped, Reason: err.Error()}
		b.record(item)
		return
	}
	item.File, item.Count = file, count
	if item.State == "" {
		item.State = cnpgReadOK
	}
	b.record(item)
}

func (b *cnpgReportBuilder) addSecret(name, by string) {
	if name == "" {
		return
	}
	if b.secrets[name] == nil {
		b.secrets[name] = map[string]bool{}
	}
	b.secrets[name][by] = true
}

func (b *cnpgReportBuilder) secretRefs() []CNPGReportSecretRef {
	out := make([]CNPGReportSecretRef, 0, len(b.secrets))
	for name, by := range b.secrets {
		out = append(out, CNPGReportSecretRef{Name: name, ReferencedBy: cnpgSortedKeys(by)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (b *cnpgReportBuilder) build(opts cnpgReportOptions) {
	namespace, name := b.cluster.GetNamespace(), b.cluster.GetName()
	selector := labels.SelectorFromSet(labels.Set{cnpgClusterLabel: name}).String()

	clusterObj := cnpgReportCleanObject(b.cluster)
	cnpgReportSecretNamesIn(clusterObj.Object, "Cluster/"+name, b.addSecret)
	b.write(CNPGReportItem{Item: "Cluster"}, "manifests/cluster.yaml", clusterObj.Object, 1)

	var jobs []batchv1.Job
	jobCov := b.s.cnpgGatedRead(b.r, cnpgGrantListJobs, namespace, func() error {
		list, err := b.typed.BatchV1().Jobs(namespace).List(b.ctx, metav1.ListOptions{LabelSelector: selector})
		if err == nil {
			jobs = list.Items
		}
		return err
	})
	ownedJobs := map[string]bool{}
	var keptJobs []batchv1.Job
	for _, j := range jobs {
		if cnpgControlledBy(j.OwnerReferences, cnpgGroup, "Cluster", name, b.cluster.GetUID()) {
			ownedJobs[j.Name] = true
			cnpgReportCleanPodSpec(&j.Spec.Template.Spec)
			j.ManagedFields = nil
			keptJobs = append(keptJobs, j)
		}
	}

	var pods []corev1.Pod
	podCov := b.s.cnpgGatedRead(b.r, cnpgGrantListPods, namespace, func() error {
		list, err := b.typed.CoreV1().Pods(namespace).List(b.ctx, metav1.ListOptions{LabelSelector: selector})
		if err == nil {
			pods = list.Items
		}
		return err
	})
	var keptPods []corev1.Pod
	unowned := 0
	for _, p := range pods {
		ref := cnpgControllerRef(p.OwnerReferences)
		owned := ref != nil && ((ref.Kind == "Cluster" && ref.UID == b.cluster.GetUID()) || (ref.Kind == "Job" && ownedJobs[ref.Name]))
		if !owned {
			unowned++
			continue
		}
		for _, sref := range cnpgPodSecretNames(&p.Spec) {
			b.addSecret(sref, "Pod/"+p.Name)
		}
		cnpgReportCleanPodSpec(&p.Spec)
		p.ManagedFields = nil
		keptPods = append(keptPods, p)
	}

	if podCov.State == cnpgReadOK {
		item := CNPGReportItem{Item: "Pods"}
		if unowned > 0 {
			item.Note = fmt.Sprintf("%d Pod(s) labelled %s=%s are not owned by this Cluster or its Jobs and were left out", unowned, cnpgClusterLabel, name)
		}
		b.write(item, "manifests/cluster-pods.yaml", corev1.PodList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"}, Items: keptPods}, len(keptPods))
	} else {
		b.record(CNPGReportItem{Item: "Pods", ReadSource: podCov})
	}
	if jobCov.State == cnpgReadOK {
		b.write(CNPGReportItem{Item: "Jobs"}, "manifests/cluster-jobs.yaml", batchv1.JobList{TypeMeta: metav1.TypeMeta{APIVersion: "batch/v1", Kind: "JobList"}, Items: keptJobs}, len(keptJobs))
	} else {
		b.record(CNPGReportItem{Item: "Jobs", ReadSource: jobCov})
	}

	var pvcs []corev1.PersistentVolumeClaim
	pvcCov := b.s.cnpgGatedRead(b.r, cnpgGrantListPVCs, namespace, func() error {
		list, err := b.typed.CoreV1().PersistentVolumeClaims(namespace).List(b.ctx, metav1.ListOptions{LabelSelector: selector})
		if err == nil {
			pvcs = list.Items
		}
		return err
	})
	if pvcCov.State == cnpgReadOK {
		var kept []corev1.PersistentVolumeClaim
		for _, c := range pvcs {
			if cnpgOwnedBy(&c, b.cluster) {
				c.ManagedFields = nil
				kept = append(kept, c)
			}
		}
		b.write(CNPGReportItem{Item: "PersistentVolumeClaims"}, "manifests/cluster-pvcs.yaml", corev1.PersistentVolumeClaimList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PersistentVolumeClaimList"}, Items: kept}, len(kept))
	} else {
		b.record(CNPGReportItem{Item: "PersistentVolumeClaims", ReadSource: pvcCov})
	}

	subjects := map[string]bool{"Cluster/" + name: true}
	for _, p := range keptPods {
		subjects["Pod/"+p.Name] = true
	}
	for _, j := range keptJobs {
		subjects["Job/"+j.Name] = true
	}
	for _, c := range pvcs {
		subjects["PersistentVolumeClaim/"+c.Name] = true
	}
	for _, child := range []struct {
		item, file string
		gvr        schema.GroupVersionResource
		grant      Grant
		kind       string
	}{
		{"Backups", "manifests/backups.yaml", cnpgBackupGVR, cnpgGrantListBackups, "Backup"},
		{"ScheduledBackups", "manifests/scheduledbackups.yaml", cnpgScheduleGVR, cnpgGrantListSchedules, "ScheduledBackup"},
		{"Poolers", "manifests/poolers.yaml", cnpgPoolerGVR, cnpgGrantListPoolers, "Pooler"},
	} {
		var items []unstructured.Unstructured
		cov := b.s.cnpgGatedRead(b.r, child.grant, namespace, func() error {
			list, err := b.dyn.Resource(child.gvr).Namespace(namespace).List(b.ctx, metav1.ListOptions{})
			if err == nil {
				items = list.Items
			}
			return err
		})
		if cov.State != cnpgReadOK {
			b.record(CNPGReportItem{Item: child.item, ReadSource: cov})
			continue
		}
		var kept []any
		for i := range items {
			if cn, _, _ := unstructured.NestedString(items[i].Object, "spec", "cluster", "name"); cn != name {
				continue
			}
			clean := cnpgReportCleanObject(&items[i])
			cnpgReportSecretNamesIn(clean.Object, child.kind+"/"+clean.GetName(), b.addSecret)
			subjects[child.kind+"/"+clean.GetName()] = true
			kept = append(kept, clean.Object)
		}
		b.write(CNPGReportItem{Item: child.item}, child.file, map[string]any{"apiVersion": "v1", "kind": "List", "items": kept}, len(kept))
	}

	b.objectStores()

	var events []corev1.Event
	evCov := b.s.cnpgGatedRead(b.r, cnpgGrantListEvents, namespace, func() error {
		list, err := b.typed.CoreV1().Events(namespace).List(b.ctx, metav1.ListOptions{})
		if err == nil {
			events = list.Items
		}
		return err
	})
	if evCov.State == cnpgReadOK {
		var kept []corev1.Event
		for _, e := range events {
			if subjects[e.InvolvedObject.Kind+"/"+e.InvolvedObject.Name] {
				e.ManagedFields = nil
				kept = append(kept, e)
			}
		}
		sort.SliceStable(kept, func(i, j int) bool { return cnpgEventTime(&kept[i]).Before(cnpgEventTime(&kept[j])) })
		b.write(CNPGReportItem{Item: "Events", Note: "Events about the Cluster and the objects in this report"}, "manifests/events.yaml", corev1.EventList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "EventList"}, Items: kept}, len(kept))
	} else {
		b.record(CNPGReportItem{Item: "Events", ReadSource: evCov})
	}

	b.captured("Operator and plugins", "operator/operator.json", b.s.handleCNPGOperator, cnpgReportStripOperatorConfig)
	b.captured("Runtime (instance manager status and exporter metrics)", "runtime/runtime.json", b.s.handleCNPGClusterRuntime, nil)
	b.captured("Storage", "runtime/storage.json", b.s.handleCNPGClusterStorage, nil)

	if opts.Logs {
		b.logs(keptPods, opts)
	} else {
		b.record(CNPGReportItem{Item: "Logs", ReadSource: ReadSource{State: cnpgReadSkipped, Reason: "not requested"}})
	}
}

func (b *cnpgReportBuilder) objectStores() {
	stores := map[string]bool{}
	if p := cnpgClusterBarmanPlugin(b.cluster); p != "" {
		stores[p] = true
	}
	ext, _, _ := unstructured.NestedSlice(b.cluster.Object, "spec", "externalClusters")
	for _, e := range ext {
		m, _ := e.(map[string]any)
		params, _, _ := unstructured.NestedStringMap(m, "plugin", "parameters")
		if params["barmanObjectName"] != "" {
			stores[params["barmanObjectName"]] = true
		}
	}
	if len(stores) == 0 {
		return
	}
	namespace := b.cluster.GetNamespace()
	var kept []any
	cov := ReadSource{State: cnpgReadOK}
	for _, name := range cnpgSortedKeys(stores) {
		var obj *unstructured.Unstructured
		c := b.s.cnpgGatedRead(b.r, cnpgGrantGetObjectStores, namespace, func() error {
			o, err := b.dyn.Resource(cnpgObjectStoreGVR).Namespace(namespace).Get(b.ctx, name, metav1.GetOptions{})
			obj = o
			return err
		})
		if c.State != cnpgReadOK {
			cov = c
			continue
		}
		clean := cnpgReportCleanObject(obj)
		cnpgReportSecretNamesIn(clean.Object, "ObjectStore/"+name, b.addSecret)
		kept = append(kept, clean.Object)
	}
	item := CNPGReportItem{Item: "ObjectStores"}
	if cov.State != cnpgReadOK {
		item.ReadSource = cov
		if len(kept) == 0 {
			b.record(item)
			return
		}
		item.State = "partial"
	}
	b.write(item, "manifests/objectstores.yaml", map[string]any{"apiVersion": "v1", "kind": "List", "items": kept}, len(kept))
}

func cnpgClusterBarmanPlugin(cluster *unstructured.Unstructured) string {
	plugins, _, _ := unstructured.NestedSlice(cluster.Object, "spec", "plugins")
	for _, p := range plugins {
		m, _ := p.(map[string]any)
		if m == nil || m["name"] != "barman-cloud.cloudnative-pg.io" {
			continue
		}
		params, _, _ := unstructured.NestedStringMap(m, "parameters")
		return params["barmanObjectName"]
	}
	return ""
}

// captured runs one of Radar's own read handlers for this Cluster, as the
// caller, and stores its JSON answer verbatim: the bundle then holds exactly
// what the UI showed, with the same gates and coverage.
func (b *cnpgReportBuilder) captured(item, file string, handler http.HandlerFunc, transform func([]byte) []byte) {
	rec := &cnpgCaptureWriter{header: http.Header{}, status: http.StatusOK}
	handler(rec, b.r)
	body := rec.body.Bytes()
	if rec.status != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		state := cnpgReadError
		switch rec.status {
		case http.StatusForbidden:
			state = cnpgReadDenied
		case http.StatusNotFound:
			state = cnpgReadNotFound
		}
		b.record(CNPGReportItem{Item: item, ReadSource: ReadSource{State: state, Reason: strings.TrimSpace(fmt.Sprintf("HTTP %d %s", rec.status, e.Error))}})
		return
	}
	if transform != nil {
		body = transform(body)
	}
	var pretty bytes.Buffer
	if json.Indent(&pretty, body, "", "  ") == nil {
		body = pretty.Bytes()
	}
	b.writeBytes(CNPGReportItem{Item: item}, file, body, nil)
}

type cnpgCaptureWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (c *cnpgCaptureWriter) Header() http.Header         { return c.header }
func (c *cnpgCaptureWriter) Write(p []byte) (int, error) { return c.body.Write(p) }
func (c *cnpgCaptureWriter) WriteHeader(status int)      { c.status = status }

// The operator's ConfigMap values are left out, as kubectl cnpg's operator
// report redacts them by default; the references stay.
func cnpgReportStripOperatorConfig(body []byte) []byte {
	var resp map[string]any
	if json.Unmarshal(body, &resp) != nil {
		return body
	}
	if cfg, ok := resp["config"].([]any); ok {
		for _, c := range cfg {
			if m, ok := c.(map[string]any); ok {
				if data, ok := m["data"].(map[string]any); ok {
					keys := make([]string, 0, len(data))
					for k := range data {
						keys = append(keys, k)
					}
					sort.Strings(keys)
					m["data"] = nil
					m["keys"] = keys
				}
			}
		}
	}
	out, err := json.Marshal(resp)
	if err != nil {
		return body
	}
	return out
}

func (b *cnpgReportBuilder) logs(pods []corev1.Pod, opts cnpgReportOptions) {
	namespace := b.cluster.GetNamespace()
	if b.s.grantPermission(b.r, cnpgGrantGetPodLogs.In(namespace)) == permissionDenied {
		b.record(CNPGReportItem{Item: "Logs", ReadSource: ReadSource{State: cnpgReadDenied, Grant: cnpgGrantGetPodLogs.In(namespace).Ref()}})
		return
	}
	note := "query text inside PostgreSQL log records is omitted"
	if opts.QueryText {
		note = "includes query text from PostgreSQL log records"
	}
	for _, p := range pods {
		containers := append(append([]corev1.Container{}, p.Spec.InitContainers...), p.Spec.Containers...)
		restarts := map[string]int32{}
		for _, st := range append(append([]corev1.ContainerStatus{}, p.Status.InitContainerStatuses...), p.Status.ContainerStatuses...) {
			restarts[st.Name] = st.RestartCount
		}
		for _, c := range containers {
			b.containerLog(p, c.Name, false, opts, note)
			if restarts[c.Name] > 0 {
				b.containerLog(p, c.Name, true, opts, note)
			}
		}
	}
}

func (b *cnpgReportBuilder) containerLog(p corev1.Pod, container string, previous bool, opts cnpgReportOptions, note string) {
	suffix := ""
	if previous {
		suffix = "-previous"
	}
	item := CNPGReportItem{Item: fmt.Sprintf("Logs %s/%s%s", p.Name, container, suffix), Note: note}
	if b.z.used >= b.z.limit {
		b.index.Truncated = true
		item.ReadSource = ReadSource{State: cnpgReadSkipped, Reason: errCNPGReportFull.Error()}
		b.record(item)
		return
	}
	limit := int64(cnpgReportLogCap)
	tail := opts.TailLines
	stream, err := b.typed.CoreV1().Pods(p.Namespace).GetLogs(p.Name, &corev1.PodLogOptions{
		Container: container, Previous: previous, Timestamps: true, TailLines: &tail, LimitBytes: &limit,
	}).Stream(b.ctx)
	if err != nil {
		item.ReadSource = cnpgReadOutcome(err, cnpgGrantGetPodLogs, p.Namespace)
		b.record(item)
		return
	}
	defer stream.Close()
	var out bytes.Buffer
	scanner := bufio.NewScanner(io.LimitReader(stream, limit))
	scanner.Buffer(make([]byte, 64<<10), int(limit))
	lines := 0
	for scanner.Scan() {
		out.WriteString(cnpgReportLogLine(scanner.Text(), opts.QueryText))
		out.WriteByte('\n')
		lines++
	}
	if err := scanner.Err(); err != nil {
		item.Note = strings.TrimSpace(item.Note + "; read stopped early: " + err.Error())
	}
	b.writeBytes(item, fmt.Sprintf("logs/%s-%s%s.log", p.Name, container, suffix), out.Bytes(), &lines)
}

var cnpgQueryRecordKeys = []string{"query", "internal_query", "context"}

// cnpgSQLMessage finds where SQL starts in a PostgreSQL log message: after
// "statement: ", "execute <name>[/<portal>]: ", "execute fetch from <name>: ",
// "parse <name>: ", "bind <name>[/<portal>]: " (each possibly after
// "duration: … ms  "), and auto_explain's "plan:". Unanchored on purpose: a
// false positive only redacts more.
var cnpgSQLMessage = regexp.MustCompile(`(?s)\b(?:statement|execute fetch from \S+|execute \S+|parse \S+|bind \S+|plan):\s?`)

// cnpgSQLProtocolMessage finds an extended-protocol message: after an
// optional "duration: … ms", text starting with execute, parse or bind. The
// statement or portal name is any string the client chose (spaces and colons
// included), so everything after the keyword goes.
var cnpgSQLProtocolMessage = regexp.MustCompile(`(?is)(?:^|\b(?:LOG|DEBUG[1-5]?|INFO|NOTICE|WARNING|ERROR|FATAL|PANIC):)\s*(?:duration:\s*\S+\s*ms\s+)?(?:execute|parse|bind)\b`)

// cnpgSQLParameters finds bind values ("parameters: $1 = '…'"), in a DETAIL
// field or line.
var cnpgSQLParameters = regexp.MustCompile(`(?is)\bparameters:\s?`)

func cnpgRedactSQLText(msg string) (string, bool) {
	changed := false
	for _, re := range []*regexp.Regexp{cnpgSQLProtocolMessage, cnpgSQLMessage, cnpgSQLParameters} {
		if loc := re.FindStringIndex(msg); loc != nil {
			msg = msg[:loc[1]] + cnpgReportQueryRedacted
			changed = true
		}
	}
	return msg, changed
}

// cnpgReportLogLine redacts one log line. PostgreSQL records from the instance
// manager are JSON with the statement in record.query / record.message and
// bind parameters in record.detail; without the query-text opt-in those are
// replaced, and plain-text lines are cut where SQL starts. Every line also
// gets the high-confidence secret patterns.
func cnpgReportLogLine(line string, queryText bool) string {
	ts, body := "", line
	if i := strings.IndexByte(line, ' '); i > 0 && i < 40 && strings.HasPrefix(strings.TrimSpace(line[i:]), "{") {
		ts, body = line[:i+1], line[i+1:]
	}
	if !queryText {
		body = cnpgRedactQueryText(body)
	}
	return ts + aicontext.RedactSecrets(body)
}

func cnpgRedactQueryText(body string) string {
	var entry map[string]any
	if !strings.HasPrefix(body, "{") || json.Unmarshal([]byte(body), &entry) != nil {
		out, _ := cnpgRedactSQLText(body)
		return out
	}
	changed := false
	redact := func(m map[string]any, key string) {
		if v, ok := m[key].(string); ok {
			if out, did := cnpgRedactSQLText(v); did {
				m[key], changed = out, true
			}
		}
	}
	if rec, ok := entry["record"].(map[string]any); ok {
		for _, k := range cnpgQueryRecordKeys {
			if v, ok := rec[k].(string); ok && v != "" {
				rec[k] = cnpgReportQueryRedacted
				changed = true
			}
		}
		redact(rec, "message")
		redact(rec, "detail")
		redact(rec, "hint")
	}
	redact(entry, "msg")
	if !changed {
		return body
	}
	out, err := json.Marshal(entry)
	if err != nil {
		return cnpgReportQueryRedacted
	}
	return string(out)
}

// cnpgReportCleanObject drops managedFields and the last-applied copy and
// blanks sensitive env values. The rest of the spec stays verbatim: CNPG
// kinds name Secrets through {name, key} selectors, and the generic inline
// redaction would blank those names (secretAccessKey: {name: ...}).
func cnpgReportCleanObject(obj *unstructured.Unstructured) *unstructured.Unstructured {
	clean := obj.DeepCopy()
	unstructured.RemoveNestedField(clean.Object, "metadata", "managedFields")
	unstructured.RemoveNestedField(clean.Object, "metadata", "annotations", "kubectl.kubernetes.io/last-applied-configuration")
	cnpgReportRedactEnvLists(clean.Object)
	return clean
}

// cnpgReportRedactEnvLists blanks sensitive env values wherever an object
// declares them — Cluster spec.env, a Pooler's pod template — so a manifest
// never shows what the Pods it produces have redacted.
func cnpgReportRedactEnvLists(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			if list, ok := child.([]any); ok && k == "env" {
				for _, item := range list {
					e, ok := item.(map[string]any)
					if !ok {
						continue
					}
					name, _ := e["name"].(string)
					if val, _ := e["value"].(string); val != "" && aicontext.IsSensitiveEnvName(name) {
						e["value"] = cnpgReportRedacted
					}
				}
				continue
			}
			cnpgReportRedactEnvLists(child)
		}
	case []any:
		for _, child := range t {
			cnpgReportRedactEnvLists(child)
		}
	}
}

func cnpgReportCleanContainers(cs []corev1.Container) {
	for i := range cs {
		for j := range cs[i].Env {
			if cs[i].Env[j].Value != "" && aicontext.IsSensitiveEnvName(cs[i].Env[j].Name) {
				cs[i].Env[j].Value = cnpgReportRedacted
			}
		}
	}
}

func cnpgReportCleanPodSpec(spec *corev1.PodSpec) {
	cnpgReportCleanContainers(spec.InitContainers)
	cnpgReportCleanContainers(spec.Containers)
}

func cnpgPodSecretNames(spec *corev1.PodSpec) []string {
	var out []string
	for _, v := range spec.Volumes {
		if v.Secret != nil {
			out = append(out, v.Secret.SecretName)
		}
		if v.Projected != nil {
			for _, src := range v.Projected.Sources {
				if src.Secret != nil {
					out = append(out, src.Secret.Name)
				}
			}
		}
	}
	for _, c := range append(append([]corev1.Container{}, spec.InitContainers...), spec.Containers...) {
		for _, e := range c.Env {
			if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
				out = append(out, e.ValueFrom.SecretKeyRef.Name)
			}
		}
		for _, ef := range c.EnvFrom {
			if ef.SecretRef != nil {
				out = append(out, ef.SecretRef.Name)
			}
		}
	}
	for _, s := range spec.ImagePullSecrets {
		out = append(out, s.Name)
	}
	return out
}

// cnpgReportSecretNamesIn finds Secret references in a CNPG object's spec:
// a {name} under a key that says secret (superuserSecret, bootstrap
// initdb.secret), a string under a key ending in secret
// (certificates.serverTLSSecret) or a {name, key} selector that is not a
// ConfigMap's (ObjectStore credentials). It records names only.
func cnpgReportSecretNamesIn(obj map[string]any, by string, add func(name, by string)) {
	var walk func(node any, under string)
	walk = func(node any, under string) {
		switch v := node.(type) {
		case map[string]any:
			name, hasName := v["name"].(string)
			_, hasKey := v["key"].(string)
			if hasName && (under == "secret" || (hasKey && under != "configmap")) {
				add(name, by)
			}
			for k, child := range v {
				lower := strings.ToLower(k)
				if s, ok := child.(string); ok && strings.HasSuffix(lower, "secret") {
					add(s, by)
					continue
				}
				next := under
				switch {
				case strings.Contains(lower, "configmap"):
					next = "configmap"
				case strings.Contains(lower, "secret"):
					next = "secret"
				}
				walk(child, next)
			}
		case []any:
			for _, child := range v {
				walk(child, under)
			}
		}
	}
	if spec, ok := obj["spec"]; ok {
		walk(spec, "")
	}
}
