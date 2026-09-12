package prom

import (
	"fmt"
	"regexp"
	"strconv"
	"time"
)

const DefaultBeylaJobSelector = `job=~".*beyla.*|.*alloy.*"`

type RequestSource string

const (
	RequestSourceBeyla RequestSource = "beyla"
	RequestSourceIstio RequestSource = "istio"
)

type RequestQueries struct {
	Rate              string
	Errors            string
	P50               string
	P95               string
	HistogramCoverage string
	StatusCoverage    string
	ObservedPods      string
}

var beylaJobMatcher = regexp.MustCompile(`^job\s*(=|=~)\s*("(?:[^"\\]|\\.)*")$`)

func BuildRequestQueries(step time.Duration, sel PodSelection, scope WorkloadMetricsScope, source RequestSource, jobSelector string) (RequestQueries, error) {
	cluster, err := scope.Matchers()
	if err != nil {
		return RequestQueries{}, err
	}
	if sel.IsEmpty() {
		return RequestQueries{}, fmt.Errorf("no current pods")
	}
	var metric, target, status, podLabel string
	unitScale := ""
	switch source {
	case RequestSourceBeyla:
		if jobSelector == "" {
			jobSelector = DefaultBeylaJobSelector
		}
		match := beylaJobMatcher.FindStringSubmatch(jobSelector)
		if match == nil {
			return RequestQueries{}, fmt.Errorf("Beyla workload charts require one exact or regex job matcher")
		}
		value, err := strconv.Unquote(match[2])
		if err != nil {
			return RequestQueries{}, fmt.Errorf("invalid Beyla job matcher: %w", err)
		}
		if match[1] == "=~" {
			if _, err := regexp.Compile(value); err != nil {
				return RequestQueries{}, fmt.Errorf("invalid Beyla job regex: %w", err)
			}
		}
		metric = "http_server_request_duration_seconds"
		target = fmt.Sprintf(`%s,k8s_namespace_name=%s,k8s_pod_name=~'%s'`, jobSelector, strconv.Quote(sel.Namespace), exactSetPattern(sel.Pods))
		status = "http_response_status_code"
		podLabel = "k8s_pod_name"
	case RequestSourceIstio:
		metric = "istio_request_duration_milliseconds"
		// A destination reporter's scrape Pod is the observed Pod. A waypoint's
		// scrape Pod is the proxy, not its target; it cannot use this attribution.
		target = fmt.Sprintf(`reporter="destination",request_protocol="http",destination_workload_namespace=%s,namespace=%s%s`, strconv.Quote(sel.Namespace), strconv.Quote(sel.Namespace), scopeClause(sel))
		status = "response_code"
		podLabel = "pod"
		unitScale = " / 1000"
	default:
		return RequestQueries{}, fmt.Errorf("unsupported request source %q", source)
	}
	countMetric := metric + "_count"
	if source == RequestSourceIstio {
		countMetric = "istio_requests_total"
	}
	rate := func(metric, extra string) string {
		return "sum(max without (job,instance) (rate(" + workloadMetricSelector(metric, cluster, target+extra) + "[" + WorkloadRateWindow(step).String() + "])))"
	}
	total := rate(countMetric, "")
	errors := rate(countMetric, fmt.Sprintf(`,%s=~"5.."`, status))
	bucketRates := "max without (job,instance) (rate(" + workloadMetricSelector(metric+"_bucket", cluster, target) + "[" + WorkloadRateWindow(step).String() + "]))"
	buckets := "sum by (le) (" + bucketRates + ")"
	bucketPopulation := "count by (le) (" + bucketRates + ")"
	quantile := func(q string) string {
		return "histogram_quantile(" + q + ", " + buckets + ")" + unitScale
	}
	return RequestQueries{
		Rate: total,
		// Zero-fill only against a successful, matching denominator. 0/0 is a
		// gap: an idle server does not have a defined error percentage.
		Errors:            "100 * (" + errors + " or (0 * " + total + ")) / " + total,
		P50:               quantile("0.50"),
		P95:               quantile("0.95"),
		HistogramCoverage: "(" + rate(metric+"_bucket", `,le="+Inf"`) + " / " + total + ") and on() (min(" + bucketPopulation + ") == max(" + bucketPopulation + "))",
		StatusCoverage:    rate(countMetric, fmt.Sprintf(`,%s=~"0|[1-5][0-9][0-9]"`, status)) + " / " + total,
		ObservedPods:      "count(count by (" + podLabel + ") (rate(" + workloadMetricSelector(countMetric, cluster, target) + "[" + WorkloadRateWindow(step).String() + "])))",
	}, nil
}
