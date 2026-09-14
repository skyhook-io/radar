package prometheus

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
)

type workloadAttribution struct {
	Scope  prom.WorkloadMetricsScope
	Pods   []prom.WorkloadPodIdentity
	UID    bool
	Job    string
	Reason string
	State  string
}

type workloadAttributions map[string]workloadAttribution

func identitySelection(namespace string, pods []prom.WorkloadPodIdentity) prom.PodSelection {
	names := make([]string, len(pods))
	for i, pod := range pods {
		names[i] = pod.Name
	}
	return prom.SelectPods(namespace, names)
}

var partitionLabels = []string{"cluster", "cluster_name", "k8s_cluster_name", "kubernetes_cluster", "cluster_id"}

func probeWorkloadAttributions(ctx context.Context, client *prom.Client, scope PodScope) (workloadAttributions, error) {
	result := workloadAttributions{}
	families := map[string]string{"cpu": "container_cpu_usage_seconds_total", "memory": "container_memory_working_set_bytes", "throttling": "container_cpu_cfs_periods_total", "beyla": "http_server_request_duration_seconds_count", "istio": "istio_requests_total"}
	names := make([]string, len(scope.Identities))
	for i, pod := range scope.Identities {
		names[i] = regexp.QuoteMeta(pod.Name)
	}
	pattern := strconv.Quote(strings.Join(names, "|"))
	ns := strconv.Quote(scope.Namespace)
	projection := "__name__,namespace,pod,k8s_namespace_name,k8s_pod_name,k8s_pod_uid,id,job,replica,prometheus_replica,__replica__," + strings.Join(partitionLabels, ",")
	selector := `{__name__=~"container_(cpu_usage_seconds_total|memory_working_set_bytes|cpu_cfs_periods_total|cpu_cfs_throttled_periods_total)",namespace=` + ns + `,pod=~` + pattern + `,container!="",container!="POD"}`
	selector += ` or {__name__=~"http_server_request_duration_seconds_(count|bucket)",k8s_namespace_name=` + ns + `,k8s_pod_name=~` + pattern + `}`
	selector += ` or {__name__=~"istio_requests_total|istio_request_duration_milliseconds_bucket",namespace=` + ns + `,pod=~` + pattern + `,reporter="destination",request_protocol="http",destination_workload_namespace=` + ns + `}`
	data, err := client.QueryEvidence(ctx, "topk by (__name__) (1025,count by ("+projection+") ("+selector+"))")
	if errors.Is(err, prom.ErrWorkloadQueryTooLarge) {
		for key := range families {
			result[key] = workloadAttribution{State: "unavailable", Reason: err.Error()}
		}
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	if data.ResultType != "vector" || len(data.Series) > 8200 {
		return nil, fmt.Errorf("source identity evidence exceeded its bound")
	}
	groups := map[string][]prom.Series{}
	for _, series := range data.Series {
		groups[series.Labels["__name__"]] = append(groups[series.Labels["__name__"]], series)
	}
	// Counters and their supporting buckets/denominators must describe one observer population.
	groups["http_server_request_duration_seconds_count"] = append(groups["http_server_request_duration_seconds_count"], groups["http_server_request_duration_seconds_bucket"]...)
	groups["istio_requests_total"] = append(groups["istio_requests_total"], groups["istio_request_duration_milliseconds_bucket"]...)
	groups["container_cpu_cfs_periods_total"] = append(groups["container_cpu_cfs_periods_total"], groups["container_cpu_cfs_throttled_periods_total"]...)
	for key, family := range families {
		rows := groups[family]
		result[key] = workloadAttribution{Reason: "No observations for current Pod names."}
		if len(rows) == 0 {
			continue
		}
		result[key] = workloadAttribution{Reason: "Metrics exist, but their cluster identity could not be established automatically."}
		counts := map[string]int{}
		for _, row := range rows {
			counts[row.Labels["__name__"]]++
		}
		bounded := true
		for _, count := range counts {
			bounded = bounded && count <= 1024
		}
		if !bounded {
			result[key] = workloadAttribution{State: "unavailable", Reason: "Too many observation identities to attribute this source safely."}
			delete(groups, family)
			continue
		}
		if key != "istio" {
			pods := matchingUIDPods(scope.Identities, rows, key == "beyla")
			if len(pods) > 0 {
				job, ok := attributionJob(rows, pods, key == "beyla")
				if ok {
					result[key] = workloadAttribution{Pods: pods, UID: true, Job: job}
					continue
				}
				result[key] = workloadAttribution{Reason: "Multiple observation populations; metrics attribution is ambiguous."}
			}
		}
	}
	needPartition := false
	for key, family := range families {
		if !result[key].UID && len(groups[family]) > 0 {
			needPartition = true
		}
	}
	if !needPartition {
		return result, nil
	}
	inventory, err := client.QueryEvidence(ctx, "topk(257,count by (namespace,pod,uid,"+strings.Join(partitionLabels, ",")+") (kube_pod_info{namespace="+ns+",pod=~"+pattern+"}))")
	if err != nil {
		for key, plan := range result {
			if !plan.UID && len(groups[families[key]]) > 0 {
				plan.State, plan.Reason = "error", "Cluster identity lookup failed. Retrying automatically."
				if errors.Is(err, prom.ErrWorkloadQueryTooLarge) {
					plan.State, plan.Reason = "unavailable", err.Error()
				}
				result[key] = plan
			}
		}
		return result, nil
	}
	if inventory.ResultType != "vector" || len(inventory.Series) > 256 {
		return result, nil
	}
	labels, pods := identifyPartition(scope.Identities, inventory.Series)
	if len(labels) == 0 {
		return result, nil
	}
	for key, family := range families {
		if result[key].UID {
			continue
		}
		var rows []prom.Series
		conflict := false
		for _, row := range groups[family] {
			matches := true
			for label, value := range labels {
				if row.Labels[label] != value {
					matches = false
				}
			}
			if matches {
				cgroupUID := ""
				if key != "beyla" && key != "istio" {
					cgroupUID = prom.PodUIDFromCgroupID(row.Labels["id"])
				}
				for _, pod := range pods {
					if key == "beyla" && row.Labels["k8s_pod_name"] == pod.Name && row.Labels["k8s_pod_uid"] != "" && row.Labels["k8s_pod_uid"] != pod.UID {
						conflict = true
					}
					if key != "beyla" && key != "istio" && row.Labels["pod"] == pod.Name {
						if cgroupUID != "" && cgroupUID != pod.UID {
							conflict = true
						}
					}
				}
				rows = append(rows, row)
			}
		}
		if conflict {
			result[key] = workloadAttribution{Reason: "Source Pod identities contradict the inferred cluster partition."}
			continue
		}
		if len(rows) == 0 {
			continue
		}
		job, ok := attributionJob(rows, nil, key == "beyla")
		if !ok {
			result[key] = workloadAttribution{Reason: "Multiple observation populations; metrics attribution is ambiguous."}
			continue
		}
		result[key] = workloadAttribution{Scope: prom.WorkloadMetricsScope{ClusterLabels: labels}, Pods: pods, Job: job}
	}
	return result, nil
}

func matchingUIDPods(pods []prom.WorkloadPodIdentity, rows []prom.Series, beyla bool) []prom.WorkloadPodIdentity {
	var result []prom.WorkloadPodIdentity
	for _, pod := range pods {
		pattern, err := prom.PodCgroupPattern(pod.UID)
		if err != nil {
			continue
		}
		re := regexp.MustCompile("^(?:" + pattern + ")$")
		for _, row := range rows {
			if beyla && row.Labels["k8s_pod_name"] == pod.Name && row.Labels["k8s_pod_uid"] == pod.UID || !beyla && row.Labels["pod"] == pod.Name && re.MatchString(row.Labels["id"]) {
				result = append(result, pod)
				break
			}
		}
	}
	return result
}

func attributionJob(rows []prom.Series, pods []prom.WorkloadPodIdentity, beyla bool) (string, bool) {
	jobs := map[string]bool{}
	replicas := map[string]map[string]bool{}
	patterns := map[string]*regexp.Regexp{}
	uids := map[string]string{}
	for _, pod := range pods {
		pattern, err := prom.PodCgroupPattern(pod.UID)
		if err == nil {
			patterns[pod.Name] = regexp.MustCompile("^(?:" + pattern + ")$")
			uids[pod.Name] = pod.UID
		}
	}
	for _, row := range rows {
		if pods != nil {
			if beyla {
				uid := uids[row.Labels["k8s_pod_name"]]
				if uid == "" || uid != row.Labels["k8s_pod_uid"] {
					continue
				}
			} else {
				re := patterns[row.Labels["pod"]]
				if re == nil || !re.MatchString(row.Labels["id"]) {
					continue
				}
			}
		}
		jobs[row.Labels["job"]] = true
		for _, label := range []string{"replica", "prometheus_replica", "__replica__"} {
			if replicas[label] == nil {
				replicas[label] = map[string]bool{}
			}
			replicas[label][row.Labels[label]] = true
		}
	}
	for _, values := range replicas {
		if len(values) > 1 {
			return "", false
		}
	}
	if len(jobs) != 1 {
		return "", false
	}
	for job := range jobs {
		return "job=" + strconv.Quote(job), true
	}
	return "", false
}

func identifyPartition(pods []prom.WorkloadPodIdentity, rows []prom.Series) (map[string]string, []prom.WorkloadPodIdentity) {
	var candidates []map[string]string
	seen := map[string]bool{}
	addCandidate := func(candidate map[string]string) {
		key := fmt.Sprint(candidate)
		if !seen[key] {
			seen[key] = true
			candidates = append(candidates, candidate)
		}
	}
	for _, row := range rows {
		for _, pod := range pods {
			if row.Labels["pod"] != pod.Name || row.Labels["uid"] != pod.UID {
				continue
			}
			for _, label := range partitionLabels {
				if value := row.Labels[label]; value != "" {
					addCandidate(map[string]string{label: value})
					for _, second := range partitionLabels {
						if second > label && row.Labels[second] != "" {
							addCandidate(map[string]string{label: value, second: row.Labels[second]})
						}
					}
				}
			}
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if len(candidates[i]) != len(candidates[j]) {
			return len(candidates[i]) < len(candidates[j])
		}
		return fmt.Sprint(candidates[i]) < fmt.Sprint(candidates[j])
	})
	var selected map[string]string
	var selectedPods []prom.WorkloadPodIdentity
	var selectedPopulation string
	for _, candidate := range candidates {
		var matched []prom.WorkloadPodIdentity
		var population []string
		conflict := false
		for _, pod := range pods {
			found := false
			for _, row := range rows {
				if row.Labels["pod"] != pod.Name {
					continue
				}
				matches := true
				for label, value := range candidate {
					if row.Labels[label] != value {
						matches = false
					}
				}
				if !matches {
					continue
				}
				if row.Labels["uid"] != pod.UID {
					conflict = true
				} else {
					found = true
				}
				population = append(population, fmt.Sprint(row.Labels))
			}
			if found {
				matched = append(matched, pod)
			}
		}
		if conflict || len(matched) == 0 {
			continue
		}
		sort.Strings(population)
		fingerprint := strings.Join(population, "\x00")
		if selected != nil && selectedPopulation != fingerprint {
			return nil, nil
		}
		if selected == nil {
			selected = candidate
			selectedPods = matched
			selectedPopulation = fingerprint
		}
	}
	return selected, selectedPods
}

type workloadAttributionEntry struct {
	identity   string
	generation uint64
	started    time.Time
	expires    time.Time
	result     workloadAttributions
	err        error
	cancel     context.CancelFunc
}

func (c *Client) automaticWorkloadAttributions(scope PodScope) (workloadAttributions, string) {
	if len(scope.Identities) == 0 {
		return nil, "unavailable"
	}
	key := scope.Namespace + "/" + scope.Kind + "/" + scope.Name
	identity := fmt.Sprint(scope.Identities)
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.retired {
		return nil, "detecting"
	}
	if c.workloadAttributionEntries == nil {
		c.workloadAttributionEntries = map[string]*workloadAttributionEntry{}
	}
	entry := c.workloadAttributionEntries[key]
	var previous workloadAttributions
	var previousExpiry time.Time
	if entry != nil {
		if entry.identity == identity && entry.generation == c.discoveryGen && entry.err == nil && len(entry.result) > 0 {
			onlyNegative := true
			for _, source := range entry.result {
				if len(source.Pods) != 0 || source.UID || source.Scope.SingleCluster || len(source.Scope.ClusterLabels) != 0 {
					onlyNegative = false
					break
				}
			}
			if onlyNegative {
				previous, previousExpiry = entry.result, entry.expires
			}
		}
		if entry.identity != identity && entry.generation == c.discoveryGen && entry.err == nil && now.Before(entry.expires) {
			previous = intersectWorkloadAttributions(entry.result, scope.Identities)
			previousExpiry = entry.expires
		}
		if entry.identity == identity && entry.generation == c.discoveryGen && now.Before(entry.expires) {
			if entry.err != nil {
				return nil, "error"
			}
			if entry.cancel != nil || entry.expires.Sub(now) > time.Minute || entry.expires.Sub(entry.started) < time.Minute {
				return entry.result, "available"
			}
			previous, previousExpiry = entry.result, entry.expires
		}
		if entry.identity != identity || entry.generation != c.discoveryGen {
			if entry.cancel != nil {
				entry.cancel()
				retained := *entry
				retained.cancel = nil
				entry = &retained
				c.workloadAttributionEntries[key] = entry
			}
		}
		if now.Sub(entry.started) < 30*time.Second {
			if previous != nil {
				return previous, "available"
			}
			return nil, "detecting"
		}
		if entry.cancel != nil {
			if previous != nil {
				return previous, "available"
			}
			return nil, "detecting"
		}
	}
	active := 0
	for _, e := range c.workloadAttributionEntries {
		if e.cancel != nil {
			active++
		}
	}
	if active >= 2 {
		if previous != nil {
			return previous, "available"
		}
		return nil, "detecting"
	}
	if len(c.workloadAttributionEntries) >= 128 && entry == nil {
		oldestKey := ""
		oldest := now
		for k, e := range c.workloadAttributionEntries {
			if e.cancel == nil && e.started.Before(oldest) {
				oldest = e.started
				oldestKey = k
			}
		}
		if oldestKey == "" {
			return nil, "detecting"
		}
		delete(c.workloadAttributionEntries, oldestKey)
	}
	ctx, cancel := context.WithTimeout(context.Background(), discoveryTimeout+8*time.Second)
	entry = &workloadAttributionEntry{identity: identity, generation: c.discoveryGen, started: now, cancel: cancel, result: previous, expires: previousExpiry}
	c.workloadAttributionEntries[key] = entry
	go func() {
		defer cancel()
		var result workloadAttributions
		_, _, err := c.EnsureConnected(ctx)
		if err == nil {
			c.mu.RLock()
			var evidenceClient *prom.Client
			if c.baseURL != "" && c.discoveryGen == entry.generation && !c.retired {
				tr := prom.NewHTTPTransport(c.baseURL, c.basePath, c.httpClient)
				tr.Headers = copyHeaders(c.headers)
				tr.MaxResponseBytes = 4 << 20
				evidenceClient = prom.NewClient(tr)
			}
			c.mu.RUnlock()
			if evidenceClient != nil {
				probeCtx, probeCancel := context.WithTimeout(ctx, 8*time.Second)
				result, err = probeWorkloadAttributions(probeCtx, evidenceClient, scope)
				probeCancel()
			} else {
				err = fmt.Errorf("metrics connection changed")
			}
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		entry.cancel = nil
		if c.retired || c.discoveryGen != entry.generation || c.workloadAttributionEntries[key] != entry {
			return
		}
		if err != nil && previous != nil && time.Now().Before(previousExpiry) {
			entry.result, entry.expires = previous, previousExpiry
			return
		}
		entry.result = result
		entry.err = err
		ttl := 5 * time.Minute
		positive := false
		for _, plan := range result {
			positive = positive || len(plan.Pods) > 0
		}
		if err != nil || !positive {
			ttl = 30 * time.Second
		}
		entry.expires = time.Now().Add(ttl)
	}()
	if previous != nil {
		return previous, "available"
	}
	return nil, "detecting"
}

func intersectWorkloadAttributions(plans workloadAttributions, pods []prom.WorkloadPodIdentity) workloadAttributions {
	current := make(map[prom.WorkloadPodIdentity]bool, len(pods))
	for _, pod := range pods {
		current[pod] = true
	}
	result := workloadAttributions{}
	matched := false
	for key, plan := range plans {
		retained := plan
		retained.Pods = nil
		for _, pod := range plan.Pods {
			if current[pod] {
				retained.Pods = append(retained.Pods, pod)
				matched = true
			}
		}
		if len(retained.Pods) == 0 && len(plan.Pods) > 0 {
			retained.Reason = "Matching replacement Pods; previous Pod identities are excluded."
		}
		result[key] = retained
	}
	if !matched {
		return nil
	}
	return result
}

func (c *Client) cancelWorkloadAttributionsLocked() {
	c.workloadPartition = nil
	for _, entry := range c.workloadAttributionEntries {
		if entry.cancel != nil {
			entry.cancel()
		}
	}
	c.workloadAttributionEntries = map[string]*workloadAttributionEntry{}
}
