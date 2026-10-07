package cnpg

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"

	auth "github.com/skyhook-io/radar/internal/auth"
	integration "github.com/skyhook-io/radar/internal/integration"
	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
	"github.com/skyhook-io/radar/pkg/cnpg"
)

const (
	reportTotalCap          = 32 << 20
	cnpgReportLogCap        = 2 << 20
	ReportDefaultTail       = 1000
	ReportMaxTail           = 10000
	ReportTimeout           = 40 * time.Second
	cnpgReportRedacted      = "[REDACTED]"
	cnpgReportQueryRedacted = "[query text omitted: download with query text included to keep it]"
)

var (
	errCNPGReportFull        = errors.New("report size bound reached")
	cnpgObjectStoreGVR       = schema.GroupVersionResource{Group: barmanGroup, Version: "v1", Resource: "objectstores"}
	cnpgGrantListBackups     = auth.Grant{Verb: "list", Group: Group, Resource: "backups"}
	cnpgGrantListSchedules   = auth.Grant{Verb: "list", Group: Group, Resource: "scheduledbackups"}
	cnpgGrantListPoolers     = auth.Grant{Verb: "list", Group: Group, Resource: "poolers"}
	cnpgGrantGetObjectStores = auth.Grant{Verb: "get", Group: barmanGroup, Resource: "objectstores"}
	cnpgGrantGetPodLogs      = auth.Grant{Verb: "get", Resource: "pods", Subresource: "log"}
	cnpgReportEnvPassword    = regexp.MustCompile(`(?i)password["']?\s*[=:]`)
)

type ReportOptions struct {
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
	integration.ReadSource
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
	Options      ReportOptions         `json:"options"`
	Contents     []CNPGReportItem      `json:"contents"`
	Secrets      []CNPGReportSecretRef `json:"secrets"`
	BoundBytes   int64                 `json:"boundBytes"`
	Truncated    bool                  `json:"truncated"`
}

type reportZip struct {
	zw    *zip.Writer
	root  string
	used  int64
	limit int64
}

func (z *reportZip) add(path string, data []byte) error {
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

func (b *cnpgReportBuilder) record(item CNPGReportItem) {
	b.index.Contents = append(b.index.Contents, item)
}

func (b *cnpgReportBuilder) write(item CNPGReportItem, file string, v any, count int) {
	data, err := yaml.Marshal(v)
	if err != nil {
		item.ReadSource = integration.ReadSource{State: cnpgReadError, Reason: err.Error()}
		b.record(item)
		return
	}
	b.writeBytes(item, file, data, &count)
}

func (b *cnpgReportBuilder) writeBytes(item CNPGReportItem, file string, data []byte, count *int) {
	if err := b.z.add(file, data); err != nil {
		b.index.Truncated = b.index.Truncated || errors.Is(err, errCNPGReportFull)
		item.ReadSource = integration.ReadSource{State: cnpgReadSkipped, Reason: err.Error()}
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

func (b *cnpgReportBuilder) build(opts ReportOptions) {
	namespace, name := b.cluster.GetNamespace(), b.cluster.GetName()
	selector := labels.SelectorFromSet(labels.Set{clusterLabel: name}).String()

	clusterObj := cnpgReportCleanObject(b.cluster)
	cnpgReportSecretNamesIn(clusterObj.Object, "Cluster/"+name, b.addSecret)
	b.write(CNPGReportItem{Item: "Cluster"}, "manifests/cluster.yaml", clusterObj.Object, 1)

	var jobs []batchv1.Job
	jobCov := b.reader.gatedRead(b.ctx, cnpgGrantListJobs, namespace, func() error {
		list, err := b.typed.BatchV1().Jobs(namespace).List(b.ctx, metav1.ListOptions{LabelSelector: selector})
		if err == nil {
			jobs = list.Items
		}
		return err
	})
	ownedJobs := map[string]bool{}
	var keptJobs []batchv1.Job
	for _, j := range jobs {
		if controlledBy(j.OwnerReferences, Group, "Cluster", name, b.cluster.GetUID()) {
			ownedJobs[j.Name] = true
			cnpgReportCleanPodSpec(&j.Spec.Template.Spec)
			j.ManagedFields = nil
			keptJobs = append(keptJobs, j)
		}
	}

	var pods []corev1.Pod
	podCov := b.reader.gatedRead(b.ctx, GrantListPods, namespace, func() error {
		list, err := b.typed.CoreV1().Pods(namespace).List(b.ctx, metav1.ListOptions{LabelSelector: selector})
		if err == nil {
			pods = list.Items
		}
		return err
	})
	var keptPods []corev1.Pod
	unowned := 0
	for _, p := range pods {
		ref := controllerRef(p.OwnerReferences)
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
			item.Note = fmt.Sprintf("%d Pod(s) labelled %s=%s are not owned by this Cluster or its Jobs and were left out", unowned, clusterLabel, name)
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
	pvcCov := b.reader.gatedRead(b.ctx, cnpgGrantListPVCs, namespace, func() error {
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
		grant      auth.Grant
		kind       string
	}{
		{"Backups", "manifests/backups.yaml", cnpgBackupGVR, cnpgGrantListBackups, "Backup"},
		{"ScheduledBackups", "manifests/scheduledbackups.yaml", ScheduleGVR, cnpgGrantListSchedules, "ScheduledBackup"},
		{"Poolers", "manifests/poolers.yaml", cnpgPoolerGVR, cnpgGrantListPoolers, "Pooler"},
	} {
		var items []unstructured.Unstructured
		cov := b.reader.gatedRead(b.ctx, child.grant, namespace, func() error {
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
	evCov := b.reader.gatedRead(b.ctx, cnpgGrantListEvents, namespace, func() error {
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

	operator, err := b.reader.Operator(b.ctx)
	b.readJSON("Operator and plugins", "operator/operator.json", cnpgReportOperator(operator), err)
	runtime, err := b.reader.ClusterRuntime(b.ctx, namespace, name)
	b.readJSON("Runtime (instance manager status and exporter metrics)", "runtime/runtime.json", runtime, err)
	storage, err := b.reader.ClusterStorage(b.ctx, namespace, name)
	b.readJSON("Storage", "runtime/storage.json", storage, err)

	if opts.Logs {
		b.logs(keptPods, opts)
	} else {
		b.record(CNPGReportItem{Item: "Logs", ReadSource: integration.ReadSource{State: cnpgReadSkipped, Reason: "not requested"}})
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
	cov := integration.ReadSource{State: cnpgReadOK}
	for _, name := range cnpgSortedKeys(stores) {
		var obj *unstructured.Unstructured
		c := b.reader.gatedRead(b.ctx, cnpgGrantGetObjectStores, namespace, func() error {
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
	for _, p := range cnpg.ParseBackupDeclaration(cluster).Plugins {
		if p.Name == cnpg.BarmanPluginName {
			return p.ObjectStore
		}
	}
	return ""
}

func (b *cnpgReportBuilder) readJSON(item, file string, value any, err error) {
	if err != nil {
		status := http.StatusInternalServerError
		var failure *ReadFailure
		if errors.As(err, &failure) {
			status = failure.Status
		}
		if errors.Is(err, ErrCNPGDisconnected) {
			status = http.StatusServiceUnavailable
		}
		state := cnpgReadError
		switch status {
		case http.StatusForbidden:
			state = cnpgReadDenied
		case http.StatusNotFound:
			state = cnpgReadNotFound
		}
		b.record(CNPGReportItem{Item: item, ReadSource: integration.ReadSource{State: state, Reason: fmt.Sprintf("HTTP %d %s", status, err)}})
		return
	}
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		b.record(CNPGReportItem{Item: item, ReadSource: integration.ReadSource{State: cnpgReadError, Reason: err.Error()}})
		return
	}
	b.writeBytes(CNPGReportItem{Item: item}, file, body, nil)
}

type cnpgReportOperatorConfig struct {
	CNPGOperatorConfigRef
	Keys *[]string `json:"keys,omitempty"`
}

type cnpgReportOperatorResponse struct {
	*CNPGOperatorResponse
	Config []cnpgReportOperatorConfig `json:"config"`
}

// Operator reports retain configuration references and keys, never their values.
func cnpgReportOperator(resp *CNPGOperatorResponse) *cnpgReportOperatorResponse {
	if resp == nil {
		return nil
	}
	config := make([]cnpgReportOperatorConfig, len(resp.Config))
	for i, c := range resp.Config {
		config[i].CNPGOperatorConfigRef = c
		if c.CNPGOperatorConfigMapState != nil && c.Data != nil {
			keys := make([]string, 0, len(c.Data))
			for k := range c.Data {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			state := *c.CNPGOperatorConfigMapState
			state.Data = nil
			config[i].CNPGOperatorConfigMapState = &state
			config[i].Keys = &keys
		}
	}
	return &cnpgReportOperatorResponse{resp, config}
}

func (b *cnpgReportBuilder) logs(pods []corev1.Pod, opts ReportOptions) {
	namespace := b.cluster.GetNamespace()
	if b.reader.Access.Permission(b.ctx, cnpgGrantGetPodLogs.In(namespace)) == integration.PermissionDenied {
		b.record(CNPGReportItem{Item: "Logs", ReadSource: integration.ReadSource{State: cnpgReadDenied, Grant: cnpgGrantGetPodLogs.In(namespace).Ref()}})
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

func (b *cnpgReportBuilder) containerLog(p corev1.Pod, container string, previous bool, opts ReportOptions, note string) {
	suffix := ""
	if previous {
		suffix = "-previous"
	}
	item := CNPGReportItem{Item: fmt.Sprintf("Logs %s/%s%s", p.Name, container, suffix), Note: note}
	if b.z.used >= b.z.limit {
		b.index.Truncated = true
		item.ReadSource = integration.ReadSource{State: cnpgReadSkipped, Reason: errCNPGReportFull.Error()}
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

// Generic inline redaction would blank Secret selector names too. Scope it
// to connection parameters; initialization SQL can contain role passwords
// and is always withheld, independently of the log query-text option.
func cnpgReportCleanObject(obj *unstructured.Unstructured) *unstructured.Unstructured {
	clean := obj.DeepCopy()
	unstructured.RemoveNestedField(clean.Object, "metadata", "managedFields")
	unstructured.RemoveNestedField(clean.Object, "metadata", "annotations", "kubectl.kubernetes.io/last-applied-configuration")
	cnpgReportRedactManifest(clean.Object)
	return clean
}

func cnpgReportRedactManifest(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			switch k {
			case "postInitSQL", "postInitApplicationSQL", "postInitTemplateSQL":
				if sql, ok := child.([]any); ok {
					for i := range sql {
						sql[i] = cnpgReportRedacted
					}
				} else {
					t[k] = cnpgReportRedacted
				}
				continue
			case "connectionParameters":
				if params, ok := child.(map[string]any); ok {
					for _, key := range []string{"password", "sslpassword"} {
						if _, present := params[key]; present {
							params[key] = cnpgReportRedacted
						}
					}
					aicontext.RedactInlineSecrets(params)
				}
			}
			if list, ok := child.([]any); ok && k == "env" {
				for _, item := range list {
					e, ok := item.(map[string]any)
					if !ok {
						continue
					}
					name, _ := e["name"].(string)
					if val, ok := e["value"].(string); ok {
						e["value"] = cnpgReportEnvValue(name, val)
					}
				}
				continue
			}
			cnpgReportRedactManifest(child)
		}
	case []any:
		for _, child := range t {
			cnpgReportRedactManifest(child)
		}
	}
}

func cnpgReportEnvValue(name, value string) string {
	decoded, err := url.QueryUnescape(value)
	if value != "" && (aicontext.IsSensitiveEnvName(name) || cnpgReportEnvPassword.MatchString(value) || err == nil && cnpgReportEnvPassword.MatchString(decoded)) {
		return cnpgReportRedacted
	}
	return aicontext.RedactSecrets(value)
}

func cnpgReportCleanContainers(cs []corev1.Container) {
	for i := range cs {
		for j := range cs[i].Env {
			cs[i].Env[j].Value = cnpgReportEnvValue(cs[i].Env[j].Name, cs[i].Env[j].Value)
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

type cnpgReportBuilder struct {
	reader  *Reader
	ctx     context.Context
	dyn     dynamic.Interface
	typed   kubernetes.Interface
	cluster *unstructured.Unstructured
	z       *reportZip
	index   *CNPGReportIndex
	secrets map[string]map[string]bool
}

type ReportMetadata struct {
	Context      string
	RequestedBy  string
	RadarVersion string
	Now          time.Time
}

func (s *Reader) Report(ctx context.Context, dyn dynamic.Interface, typed kubernetes.Interface, cluster *unstructured.Unstructured, opts ReportOptions, meta ReportMetadata) ([]byte, string, error) {
	now := meta.Now.UTC()
	root := fmt.Sprintf("report_cluster_%s_%s", cluster.GetName(), now.Format(compactStamp))
	var buf bytes.Buffer
	z := &reportZip{zw: zip.NewWriter(&buf), root: root, limit: reportTotalCap}
	index := CNPGReportIndex{GeneratedAt: now.Format(time.RFC3339), RadarVersion: meta.RadarVersion, Context: meta.Context, RequestedBy: meta.RequestedBy,
		Cluster: CNPGRuntimeObjectRef{Namespace: cluster.GetNamespace(), Name: cluster.GetName(), UID: cluster.GetUID()}, Options: opts, BoundBytes: reportTotalCap}
	b := &cnpgReportBuilder{reader: s, ctx: ctx, dyn: dyn, typed: typed, cluster: cluster, z: z, index: &index, secrets: map[string]map[string]bool{}}
	b.build(opts)
	index.Secrets = b.secretRefs()
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return nil, "", err
	}
	z.limit += int64(len(data))
	if err := z.add("report.json", data); err != nil {
		return nil, "", err
	}
	if err := z.zw.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), root, nil
}
