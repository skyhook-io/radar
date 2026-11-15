package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/clusterreplay"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type subject struct {
	Kind      string `json:"kind"`
	Group     string `json:"group"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}
type request struct {
	Reader, Kind, Path string
	Subject            subject
	Tool               string
	Args               map[string]any
	Relationships      bool
}
type example struct {
	Subject subject `json:"subject"`
	A       any     `json:"a"`
	B       any     `json:"b"`
}
type delta struct {
	Reader   string    `json:"reader"`
	Field    string    `json:"field"`
	Kind     string    `json:"kind"`
	Count    int       `json:"count"`
	Examples []example `json:"examples"`
}
type timing struct {
	Reader string  `json:"reader"`
	Kind   string  `json:"kind"`
	Side   string  `json:"side"`
	Count  int     `json:"count"`
	Median float64 `json:"median_ms"`
	P95    float64 `json:"p95_ms"`
}
type failure struct {
	Reader  string  `json:"reader"`
	Subject subject `json:"subject"`
	Side    string  `json:"side"`
	Error   string  `json:"error"`
}
type report struct {
	Requests   int      `json:"request_pairs"`
	DeltaCount int      `json:"delta_count"`
	Deltas     []*delta `json:"deltas"`
	// Noise holds deltas whose (reader, field) also differed between two runs
	// of one build (-baseline): output that varies run to run, not between builds.
	NoiseCount int            `json:"noise_count,omitempty"`
	Noise      []*delta       `json:"noise,omitempty"`
	Latency    []timing       `json:"latency"`
	Failures   []failure      `json:"failures"`
	Readiness  map[string]any `json:"readiness"`
}

func main() {
	a := flag.String("a", "", "first Radar URL")
	b := flag.String("b", "", "second Radar URL")
	capture := flag.String("capture", "", "subject capture .ndjson.gz")
	out := flag.String("out", "report.json", "JSON report")
	max := flag.Int("max-per-kind", 0, "maximum subjects per group/kind (0 = all)")
	adminA := flag.String("ready-a", "", "optional replay admin URL; otherwise infer main port+1")
	adminB := flag.String("ready-b", "", "optional replay admin URL; otherwise infer main port+1")
	baseline := flag.String("baseline", "", "report from two runs of one build; fields that differed there are reported as noise")
	flag.Parse()
	if *a == "" || *b == "" || *capture == "" || *max < 0 {
		fmt.Fprintln(os.Stderr, "-a, -b, -capture required; -max-per-kind must be >= 0")
		os.Exit(1)
	}
	if err := run(*a, *b, *capture, *out, *max, *adminA, *adminB, *baseline); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(a, b, capture, out string, max int, adminA, adminB, baseline string) error {
	noise, err := loadNoise(baseline)
	if err != nil {
		return fmt.Errorf("baseline: %w", err)
	}
	f, err := os.Open(capture)
	if err != nil {
		return err
	}
	defer f.Close()
	var objects []*unstructured.Unstructured
	header, err := clusterreplay.Read(f, func(o *unstructured.Unstructured) error { objects = append(objects, o); return nil })
	if err != nil {
		return err
	}
	requests := plan(objects, header, max)
	client := &http.Client{Timeout: 65 * time.Second}
	result := report{Deltas: []*delta{}, Failures: []failure{}, Readiness: map[string]any{}}
	for _, side := range []struct{ Name, Base, Admin string }{{"a", a, adminA}, {"b", b, adminB}} {
		status, err := waitReady(client, side.Base, side.Admin)
		if err != nil {
			return fmt.Errorf("%s readiness: %w", side.Name, err)
		}
		result.Readiness[side.Name] = status
	}
	groups, noiseGroups := map[string]*delta{}, map[string]*delta{}
	times := map[string][]float64{}
	// Alternate the first side to distribute warm-cache and machine-load bias.
	for i, r := range requests {
		var values [2]any
		order := []int{0, 1}
		if i%2 != 0 {
			order = []int{1, 0}
		}
		for _, side := range order {
			base := a
			label := "a"
			if side == 1 {
				base = b
				label = "b"
			}
			start := time.Now()
			value, err := fetch(client, base, r)
			elapsed := float64(time.Since(start)) / float64(time.Millisecond)
			times[r.Reader+"\x00"+r.Kind+"\x00"+label] = append(times[r.Reader+"\x00"+r.Kind+"\x00"+label], elapsed)
			if err != nil {
				result.Failures = append(result.Failures, failure{r.Reader, r.Subject, label, err.Error()})
			}
			values[side] = normalize(value)
		}
		changes := map[string][2]any{}
		compare("", values[0], values[1], changes)
		for field, values := range changes {
			key := r.Reader + "\x00" + field + "\x00" + r.Kind
			target, count := groups, &result.DeltaCount
			if noise[r.Reader+"\x00"+fieldPattern(field)] {
				target, count = noiseGroups, &result.NoiseCount
			}
			d := target[key]
			if d == nil {
				d = &delta{Reader: r.Reader, Field: field, Kind: r.Kind}
				target[key] = d
			}
			d.Count++
			*count++
			if len(d.Examples) < 5 {
				d.Examples = append(d.Examples, example{r.Subject, values[0], values[1]})
			}
		}
		result.Requests++
	}
	for _, d := range groups {
		result.Deltas = append(result.Deltas, d)
	}
	for _, d := range noiseGroups {
		result.Noise = append(result.Noise, d)
	}
	sort.Slice(result.Noise, func(i, j int) bool {
		x, y := result.Noise[i], result.Noise[j]
		return x.Reader+"/"+x.Field+"/"+x.Kind < y.Reader+"/"+y.Field+"/"+y.Kind
	})
	sort.Slice(result.Deltas, func(i, j int) bool {
		x, y := result.Deltas[i], result.Deltas[j]
		return x.Reader+"/"+x.Field+"/"+x.Kind < y.Reader+"/"+y.Field+"/"+y.Kind
	})
	keys := make([]string, 0, len(times))
	for k := range times {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		v := times[key]
		sort.Float64s(v)
		p := strings.Split(key, "\x00")
		result.Latency = append(result.Latency, timing{p[0], p[1], p[2], len(v), percentile(v, .5), percentile(v, .95)})
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(out, append(data, '\n'), 0600); err != nil {
		return err
	}
	fmt.Printf("request pairs=%d deltas=%d groups=%d noise=%d failed calls=%d report=%s\n", result.Requests, result.DeltaCount, len(result.Deltas), result.NoiseCount, len(result.Failures), out)
	return nil
}

func plan(objects []*unstructured.Unstructured, h clusterreplay.Header, max int) []request {
	result := []request{{Reader: "applications", Kind: "*", Path: "/api/applications"}}
	seen := map[string]bool{}
	counts := map[string]int{}
	lists := map[string]bool{}
	sort.Slice(objects, func(i, j int) bool { return identity(objects[i]) < identity(objects[j]) })
	for _, o := range objects {
		gv, _ := schema.ParseGroupVersion(o.GetAPIVersion())
		s := subject{o.GetKind(), gv.Group, o.GetNamespace(), o.GetName()}
		key := identity(o)
		if seen[key] {
			continue
		}
		seen[key] = true
		kindKey := s.Kind + "." + s.Group
		ns := s.Namespace
		if ns == "" {
			ns = "_"
		}
		suffix := url.PathEscape(s.Kind) + "/" + url.PathEscape(ns) + "/" + url.PathEscape(s.Name)
		query := "?group=" + url.QueryEscape(s.Group)
		if !lists[kindKey] {
			result = append(result, request{Reader: "summary", Kind: kindKey, Subject: subject{Kind: s.Kind, Group: s.Group}, Path: "/api/resources/" + url.PathEscape(s.Kind) + query + "&include=summary"})
			lists[kindKey] = true
		}
		if (s.Kind == "Application" && s.Group == "argoproj.io") || (s.Kind == "Kustomization" && s.Group == "kustomize.toolkit.fluxcd.io") || (s.Kind == "HelmRelease" && s.Group == "helm.toolkit.fluxcd.io") {
			result = append(result, request{Reader: "gitops_tree", Kind: kindKey, Subject: s, Path: "/api/gitops/tree/" + suffix + query})
		}
		if max > 0 && counts[kindKey] >= max {
			continue
		}
		counts[kindKey]++
		args := map[string]any{"kind": s.Kind, "group": s.Group, "namespace": s.Namespace, "name": s.Name}
		result = append(result,
			request{Reader: "relationships", Kind: kindKey, Subject: s, Path: "/api/resources/" + suffix + query, Relationships: true},
			request{Reader: "neighborhood", Kind: kindKey, Subject: s, Path: "/api/ai/neighborhood/" + suffix + query},
			request{Reader: "ai_context", Kind: kindKey, Subject: s, Path: "/api/ai/resources/" + suffix + query},
			request{Reader: "mcp_get_resource", Kind: kindKey, Subject: s, Tool: "get_resource", Args: args},
			request{Reader: "mcp_get_neighborhood", Kind: kindKey, Subject: s, Tool: "get_neighborhood", Args: args})
	}
	// Empty kinds still affect collection/ownership completeness.
	for _, list := range h.Discovery.Resources {
		gv, _ := schema.ParseGroupVersion(list.GroupVersion)
		for _, r := range list.APIResources {
			k := r.Kind + "." + gv.Group
			if clusterreplay.Listable(gv, r) && !lists[k] {
				result = append(result, request{Reader: "summary", Kind: k, Subject: subject{Kind: r.Kind, Group: gv.Group}, Path: "/api/resources/" + url.PathEscape(r.Kind) + "?include=summary&group=" + url.QueryEscape(gv.Group)})
				lists[k] = true
			}
		}
	}
	return result
}

func identity(o *unstructured.Unstructured) string {
	gv, _ := schema.ParseGroupVersion(o.GetAPIVersion())
	return strings.Join([]string{gv.Group, o.GetKind(), o.GetNamespace(), o.GetName()}, "/")
}
func percentile(v []float64, p float64) float64 { return v[int(math.Ceil(float64(len(v))*p))-1] }

func waitReady(_ *http.Client, base, admin string) (any, error) {
	if admin == "" {
		u, err := url.Parse(base)
		if err != nil {
			return nil, err
		}
		var port int
		if _, err := fmt.Sscanf(u.Port(), "%d", &port); err != nil {
			return nil, fmt.Errorf("provide -ready-a/-ready-b for URLs without explicit port")
		}
		u.Host = net.JoinHostPort(u.Hostname(), fmt.Sprint(port+1))
		admin = u.String()
	}
	deadline := time.Now().Add(30 * time.Second)
	probe := &http.Client{Timeout: time.Second}
	for time.Now().Before(deadline) {
		res, err := probe.Get(strings.TrimRight(admin, "/") + "/loadtest/status")
		if err == nil {
			var v map[string]any
			err = json.NewDecoder(res.Body).Decode(&v)
			res.Body.Close()
			if err == nil && res.StatusCode == 200 && v["ready"] == true {
				return v, nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil, fmt.Errorf("no replay readiness at %s/loadtest/status", admin)
}

func fetch(c *http.Client, base string, r request) (any, error) {
	method := http.MethodGet
	path := r.Path
	var body io.Reader
	if r.Tool != "" {
		method = http.MethodPost
		path = "/mcp"
		payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": r.Tool, "arguments": r.Args}})
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequest(method, strings.TrimRight(base, "/")+path, body)
	if err != nil {
		return map[string]any{"transport_error": err.Error()}, err
	}
	req.Header.Set("Accept", "application/json, text/event-stream")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("MCP-Protocol-Version", "2025-06-18")
	}
	res, err := c.Do(req)
	if err != nil {
		return map[string]any{"transport_error": err.Error()}, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 64<<20+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 64<<20 {
		return nil, fmt.Errorf("response exceeds 64 MiB")
	}
	var value any
	if strings.Contains(res.Header.Get("Content-Type"), "text/event-stream") {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "data:") {
				if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &value) == nil {
					break
				}
			}
		}
	} else {
		_ = json.Unmarshal(data, &value)
	}
	if value == nil {
		value = string(data)
	}
	if res.StatusCode != 200 {
		return map[string]any{"status": res.StatusCode, "body": value}, fmt.Errorf("HTTP %d", res.StatusCode)
	}
	if r.Tool != "" {
		envelope, ok := value.(map[string]any)
		if !ok {
			return value, fmt.Errorf("invalid MCP envelope")
		}
		if e, ok := envelope["error"]; ok {
			return e, fmt.Errorf("MCP JSON-RPC error")
		}
		result, ok := envelope["result"].(map[string]any)
		if !ok {
			return value, fmt.Errorf("missing MCP result")
		}
		if blocks, ok := result["content"].([]any); ok {
			for _, b := range blocks {
				if block, ok := b.(map[string]any); ok {
					if text, ok := block["text"].(string); ok {
						var decoded any
						if json.Unmarshal([]byte(text), &decoded) == nil {
							block["text"] = decoded
						}
					}
				}
			}
		}
		if result["isError"] == true {
			return result, fmt.Errorf("MCP tool error")
		}
		value = result
	}
	if r.Relationships {
		m, ok := value.(map[string]any)
		if !ok {
			return value, fmt.Errorf("resource response is not an object")
		}
		relationships, ok := m["relationships"]
		if !ok {
			return value, fmt.Errorf("resource response lacks relationships")
		}
		value = relationships
	}
	return value, nil
}

var volatile = map[string]bool{"timestamp": true, "timestamps": true, "captured_at": true, "creationTimestamp": true, "deletionTimestamp": true, "lastTransitionTime": true, "lastUpdateTime": true, "lastProbeTime": true, "lastHeartbeatTime": true, "startedAt": true, "finishedAt": true, "generatedAt": true, "collectedAt": true, "observedAt": true, "computedAt": true, "duration": true, "durationMs": true, "elapsedMs": true, "requestId": true, "request_id": true, "generation": true, "observedGeneration": true, "resourceVersion": true, "age": true, "uptime": true}

func normalize(v any) any {
	switch x := v.(type) {
	case map[string]any:
		m := map[string]any{}
		for k, v := range x {
			if !volatile[k] {
				m[k] = normalize(v)
			}
		}
		return m
	case []any:
		a := make([]any, len(x))
		keyed := true
		for i, v := range x {
			a[i] = normalize(v)
			if elementKey(a[i]) == "" {
				keyed = false
			}
		}
		if keyed {
			sort.SliceStable(a, func(i, j int) bool {
				if ki, kj := elementKey(a[i]), elementKey(a[j]); ki != kj {
					return ki < kj
				}
				p, _ := json.Marshal(a[i])
				q, _ := json.Marshal(a[j])
				return string(p) < string(q)
			})
		}
		return a
	}
	return v
}

// elementKey identifies an array element the readers return in no particular
// order: a resource ref, a listed object, or a graph node or edge. "" means the
// array's order is part of the answer.
func elementKey(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	str := func(m map[string]any, k string) string { s, _ := m[k].(string); return s }
	if md, ok := m["metadata"].(map[string]any); ok && str(md, "name") != "" {
		return "obj|" + str(m, "apiVersion") + "|" + str(m, "kind") + "|" + str(md, "namespace") + "|" + str(md, "name")
	}
	if str(m, "name") != "" && (m["kind"] != nil || m["namespace"] != nil) {
		return "ref|" + str(m, "group") + "|" + str(m, "kind") + "|" + str(m, "namespace") + "|" + str(m, "name")
	}
	if str(m, "id") != "" {
		return "id|" + str(m, "id")
	}
	if m["source"] != nil && m["target"] != nil {
		return "edge|" + fmt.Sprint(m["source"]) + "|" + fmt.Sprint(m["target"]) + "|" + str(m, "type")
	}
	return ""
}

var (
	indexSegment = regexp.MustCompile(`/(\d+|\[[^\]]*\])`)
)

// fieldPattern generalizes a delta's path over array positions and element
// keys, so a baseline field matches the same field on any element.
func fieldPattern(field string) string { return indexSegment.ReplaceAllString(field, "/*") }

func loadNoise(path string) (map[string]bool, error) {
	noise := map[string]bool{}
	if path == "" {
		return noise, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var base report
	if err := json.Unmarshal(data, &base); err != nil {
		return nil, err
	}
	for _, d := range base.Deltas {
		noise[d.Reader+"\x00"+fieldPattern(d.Field)] = true
	}
	return noise, nil
}

func keyedArray(xs []any) bool {
	for _, x := range xs {
		if elementKey(x) == "" {
			return false
		}
	}
	return len(xs) > 0
}

func compare(path string, a, b any, out map[string][2]any) {
	if reflect.DeepEqual(a, b) {
		return
	}
	am, aok := a.(map[string]any)
	bm, bok := b.(map[string]any)
	if aok && bok {
		keys := map[string]bool{}
		for k := range am {
			keys[k] = true
		}
		for k := range bm {
			keys[k] = true
		}
		for k := range keys {
			p := path + "/" + strings.ReplaceAll(strings.ReplaceAll(k, "~", "~0"), "/", "~1")
			av, ae := am[k]
			bv, be := bm[k]
			if !ae || !be {
				if !ae {
					av = map[string]any{"$missing": true}
				}
				if !be {
					bv = map[string]any{"$missing": true}
				}
				out[p] = [2]any{av, bv}
			} else {
				compare(p, av, bv, out)
			}
		}
		return
	}
	aa, aok := a.([]any)
	ba, bok := b.([]any)
	if aok && bok && keyedArray(aa) && keyedArray(ba) {
		byKey := func(xs []any) map[string]any {
			m := map[string]any{}
			for _, x := range xs {
				k := elementKey(x)
				for m[k] != nil {
					k += "+"
				}
				m[k] = x
			}
			return m
		}
		am, bm := byKey(aa), byKey(ba)
		keys := map[string]bool{}
		for k := range am {
			keys[k] = true
		}
		for k := range bm {
			keys[k] = true
		}
		for k := range keys {
			p := path + "/[" + k + "]"
			av, ae := am[k]
			bv, be := bm[k]
			switch {
			case !ae:
				out[p] = [2]any{map[string]any{"$missing": true}, bv}
			case !be:
				out[p] = [2]any{av, map[string]any{"$missing": true}}
			default:
				compare(p, av, bv, out)
			}
		}
		return
	}
	if aok && bok && len(aa) == len(ba) {
		for i := range aa {
			compare(fmt.Sprintf("%s/%d", path, i), aa[i], ba[i], out)
		}
		return
	}
	if path == "" {
		path = "/"
	}
	out[path] = [2]any{a, b}
}
