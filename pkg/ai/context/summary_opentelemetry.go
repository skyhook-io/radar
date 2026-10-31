package context

import (
	"bufio"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"

	"github.com/skyhook-io/radar/pkg/health"
)

func summarizeOpenTelemetryCollector(u *unstructured.Unstructured) *ResourceSummary {
	v := health.OpenTelemetryCollector(u)
	s := &ResourceSummary{Kind: u.GetKind(), Name: u.GetName(), Namespace: u.GetNamespace(), Age: age(u.GetCreationTimestamp().Time), Status: v.Reason}
	if v.Level == health.LevelUnhealthy {
		s.Issue = "Collector reconciliation failed; inspect conditions and operator events"
	}
	s.Type, _, _ = unstructured.NestedString(u.Object, "spec", "mode")
	s.Version, _, _ = unstructured.NestedString(u.Object, "status", "version")
	s.Image, _, _ = unstructured.NestedString(u.Object, "status", "image")
	reported, _, _ := unstructured.NestedString(u.Object, "status", "scale", "statusReplicas")
	pipelines := collectorPipelineSummary(u)
	if reported != "" || pipelines != nil {
		s.OpenTelemetryCollector = &OpenTelemetryCollectorSummary{ReportedPodReadiness: reported, DeclaredPipelines: pipelines}
	}
	return s
}

func summarizeOpenTelemetryInstrumentation(u *unstructured.Unstructured) *ResourceSummary {
	v := health.OpenTelemetryInstrumentation(u)
	s := &ResourceSummary{Kind: u.GetKind(), Name: u.GetName(), Namespace: u.GetNamespace(), Age: age(u.GetCreationTimestamp().Time), Status: v.Reason}
	blocked, _, _ := unstructured.NestedMap(u.Object, "status", "upgradeBlockedVersions")
	if len(blocked) > 0 {
		names := make([]string, 0, len(blocked))
		for language := range blocked {
			names = append(names, language)
		}
		s.OpenTelemetryInstrumentation = &OpenTelemetryInstrumentationSummary{RecordedUpgradeBlocks: previewNames(names, 10)}
	}
	return s
}

func collectorPipelineSummary(u *unstructured.Unstructured) *NamePreview {
	config, found, _ := unstructured.NestedFieldNoCopy(u.Object, "spec", "config")
	if !found || config == nil {
		return nil
	}
	if text, ok := config.(string); ok {
		// Alpha Collector configs are YAML. Bound parsing work in list/search
		// summaries; full config remains available through get_resource.
		if len(text) > 64*1024 {
			return nil
		}
		var parsed map[string]any
		reader := k8syaml.NewYAMLReader(bufio.NewReader(strings.NewReader(text)))
		document, err := reader.Read()
		if err != nil {
			return nil
		}
		if _, err = reader.Read(); err != io.EOF {
			return nil
		}
		if yaml.UnmarshalStrict(document, &parsed) != nil {
			return nil
		}
		config = parsed
	}
	m, ok := config.(map[string]any)
	if !ok {
		return nil
	}
	value, found, err := unstructured.NestedFieldNoCopy(m, "service", "pipelines")
	if err != nil {
		return nil
	}
	pipelines, ok := value.(map[string]any)
	if found && !ok {
		return nil
	}
	names := make([]string, 0, len(pipelines))
	for name := range pipelines {
		names = append(names, name)
	}
	out := previewNames(names, 5)
	return &out
}

func previewNames(names []string, limit int) NamePreview {
	sort.Strings(names)
	out := NamePreview{Count: len(names)}
	if len(names) > limit {
		names = names[:limit]
		out.NamesTruncated = true
	}
	for _, name := range names {
		short := summaryName(name)
		out.Names = append(out.Names, short)
		out.NamesTruncated = out.NamesTruncated || short != name
	}
	return out
}

func summaryName(name string) string {
	if len(name) <= 160 {
		return name
	}
	end := 160
	for end > 0 && !utf8.RuneStart(name[end]) {
		end--
	}
	return strings.TrimSpace(name[:end]) + "…"
}
