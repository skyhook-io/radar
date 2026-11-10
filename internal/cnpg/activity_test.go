package cnpg

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/k8s"
	pkgtimeline "github.com/skyhook-io/radar/pkg/timeline"
)

type activityTimeline struct {
	rows, oldest       []pkgtimeline.TimelineEvent
	rowsErr, oldestErr error
	queries            []pkgtimeline.QueryOptions
	contexts           []context.Context
}

func (s *activityTimeline) Query(ctx context.Context, options pkgtimeline.QueryOptions) ([]pkgtimeline.TimelineEvent, error) {
	s.queries = append(s.queries, options)
	s.contexts = append(s.contexts, ctx)
	if options.SequenceOrder == pkgtimeline.SequenceOrderAscending {
		return s.oldest, s.oldestErr
	}
	return s.rows, s.rowsErr
}

func activityReader() *Reader {
	return &Reader{Access: Access{CanRead: func(context.Context, string, string, string, string) bool { return true }}}
}

func activityEvent(id, version, kind, name, uid string, at time.Time) pkgtimeline.TimelineEvent {
	return pkgtimeline.TimelineEvent{ID: id, APIVersion: version, Kind: kind, Name: name, Namespace: "db", UID: uid, Timestamp: at, Source: pkgtimeline.SourceInformer}
}

func activityEventIDs(response *CNPGClusterActivityResponse) []string {
	ids := make([]string, 0, len(response.Events))
	for _, event := range response.Events {
		ids = append(ids, event.ID)
	}
	return ids
}

func TestClusterActivityRetainedAttributionAndQueryScope(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	labelled := func(event pkgtimeline.TimelineEvent) pkgtimeline.TimelineEvent {
		event.Labels = map[string]string{pkgtimeline.CNPGClusterLabel: "pg"}
		return event
	}
	pod := activityEvent("pod", "v1", "Pod", "pg-1", "pod-uid", now.Add(-time.Hour))
	pod.Owner = &pkgtimeline.OwnerInfo{APIVersion: Group + "/v1", Kind: "Cluster", Name: "pg", UID: "cluster-uid"}
	podEvent := activityEvent("pod-event", "v1", "Pod", "pg-1", "pod-uid", now.Add(-30*time.Minute))
	podEvent.Source = pkgtimeline.SourceK8sEvent
	backupEvent := activityEvent("backup-event", Group+"/v1", "Backup", "deleted", "backup-uid", now.Add(-10*time.Minute))
	backupEvent.Source = pkgtimeline.SourceK8sEvent
	oldest := now.Add(-72 * time.Hour)
	oldLabel := now.Add(-48 * time.Hour)
	store := &activityTimeline{
		rows: []pkgtimeline.TimelineEvent{
			backupEvent, podEvent, pod,
			labelled(activityEvent("backup-delete", Group+"/v1", "Backup", "deleted", "backup-uid", now.Add(-40*time.Minute))),
			activityEvent("cluster", Group+"/v1", "Cluster", "pg", "cluster-uid", now.Add(-2*time.Hour)),
			labelled(activityEvent("old-pooler", Group+"/v1", "Pooler", "pg-pooler", "pooler-uid", oldLabel)),
			labelled(activityEvent("velero", "velero.io/v1", "Backup", "pg", "velero-uid", now)),
			activityEvent("capi", "cluster.x-k8s.io/v1beta1", "Cluster", "pg", "capi-uid", now),
			labelled(activityEvent("impostor", "v1", "Pod", "impostor", "impostor-uid", now)),
			labelled(activityEvent("cluster-catalog", Group+"/v1", "ClusterImageCatalog", "catalog", "catalog-uid", now)),
		},
		oldest: []pkgtimeline.TimelineEvent{activityEvent("unrelated-floor", "v1", "Secret", "unrelated", "secret-uid", oldest)},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	response, err := activityReader().ClusterActivity(ctx, store, "captured-context", "db", "pg", ActivityOptions{Since: now.Add(-24 * time.Hour), Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := activityEventIDs(response), []string{"backup-event", "pod-event", "backup-delete", "pod", "cluster"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if response.AttributionSince == nil || !response.AttributionSince.Equal(oldLabel) || response.Oldest == nil || !response.Oldest.Equal(oldest) || response.Truncated {
		t.Fatalf("retention/attribution = %+v", response)
	}
	if len(store.queries) != 2 {
		t.Fatalf("queries = %+v", store.queries)
	}
	for i, query := range store.queries {
		if store.contexts[i] != ctx || query.ClusterContext != "captured-context" || !reflect.DeepEqual(query.Namespaces, []string{"db"}) || !query.IncludeManaged || !query.IncludeK8sEvents {
			t.Fatalf("query escaped captured scope: %+v", query)
		}
		if !query.Since.IsZero() || !query.Until.IsZero() {
			t.Fatalf("window applied before retained attribution: %+v", query)
		}
	}
	scan, floor := store.queries[0], store.queries[1]
	if scan.Limit != cnpgActivityScanLimit || !reflect.DeepEqual(scan.APIGroups, []string{"", Group, barmanGroup}) || !slices.Contains(scan.Kinds, "Pod") || slices.Contains(scan.Kinds, "ClusterImageCatalog") {
		t.Fatalf("scan = %+v", scan)
	}
	if floor.Limit != 1 || floor.SequenceOrder != pkgtimeline.SequenceOrderAscending || len(floor.Kinds) != 0 || len(floor.APIGroups) != 0 {
		t.Fatalf("floor narrowed by activity kinds: %+v", floor)
	}
}

func TestClusterActivityPermissionFiltersApplyToUIDJoinedEvents(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	backup := activityEvent("backup", Group+"/v1", "Backup", "deleted", "backup-uid", now)
	backup.Labels = map[string]string{pkgtimeline.CNPGClusterLabel: "pg"}
	event := activityEvent("event", Group+"/v1", "Backup", "deleted", "backup-uid", now)
	event.Source = pkgtimeline.SourceK8sEvent
	for _, test := range []struct {
		name            string
		backups, events bool
		want            []string
	}{
		{"all", true, true, []string{"backup", "event"}},
		{"no backups", false, true, []string{}},
		{"no events", true, false, []string{"backup"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := map[string]int{}
			reader := activityReader()
			reader.Access.CanRead = func(_ context.Context, group, resource, namespace, verb string) bool {
				if namespace != "db" || verb != "list" {
					t.Fatalf("wrong grant: %s %s/%s in %s", verb, group, resource, namespace)
				}
				calls[resource]++
				if resource == "events" {
					return test.events
				}
				return resource == "backups" && group == Group && test.backups
			}
			response, err := reader.ClusterActivity(context.Background(), &activityTimeline{rows: []pkgtimeline.TimelineEvent{event, backup}}, "ctx", "db", "pg", ActivityOptions{Since: now.Add(-time.Hour), Limit: 20})
			if err != nil {
				t.Fatal(err)
			}
			if got := activityEventIDs(response); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("events = %v, want %v", got, test.want)
			}
			if calls["backups"] != 1 || calls["events"] != 1 {
				t.Fatalf("grants were not memoized by kind: %v", calls)
			}
			if !test.backups && response.AttributionSince != nil {
				t.Fatalf("denied attribution leaked: %+v", response)
			}
		})
	}
}

func TestClusterActivityWindowSortDedupAndLimit(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	row := func(id string, at time.Time) pkgtimeline.TimelineEvent {
		return activityEvent(id, Group+"/v1", "Cluster", "pg", "cluster-uid", at)
	}
	store := &activityTimeline{rows: []pkgtimeline.TimelineEvent{
		row("later", now.Add(time.Nanosecond)), row("z", now), row("a", now), row("a", now.Add(-time.Minute)), row("start", now.Add(-time.Hour)), row("before", now.Add(-time.Hour-time.Nanosecond)),
	}}
	for _, test := range []struct {
		limit     int
		want      []string
		truncated bool
	}{{20, []string{"a", "z", "start"}, false}, {2, []string{"a", "z"}, true}} {
		response, err := activityReader().ClusterActivity(context.Background(), store, "ctx", "db", "pg", ActivityOptions{Since: now.Add(-time.Hour), Until: now, Limit: test.limit})
		if err != nil {
			t.Fatal(err)
		}
		if got := activityEventIDs(response); !reflect.DeepEqual(got, test.want) || response.Truncated != test.truncated {
			t.Fatalf("events = %v truncated=%v", got, response.Truncated)
		}
	}
}

func TestClusterActivityScanCapAndEmptyWireShape(t *testing.T) {
	for _, count := range []int{0, cnpgActivityScanLimit} {
		store := &activityTimeline{rows: make([]pkgtimeline.TimelineEvent, count)}
		response, err := activityReader().ClusterActivity(context.Background(), store, "ctx", "db", "pg", ActivityOptions{Limit: 200})
		if err != nil {
			t.Fatal(err)
		}
		if response.Truncated != (count == cnpgActivityScanLimit) {
			t.Fatalf("scan %d: truncated=%v", count, response.Truncated)
		}
		body, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		want := `{"events":[],"oldest":null,"attributionSince":null,"truncated":false}`
		if count == 0 && string(body) != want {
			t.Fatalf("empty shape = %s", body)
		}
	}
}

func TestClusterActivityLiveIncarnationAndOptionalEnrichmentFailure(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	row := func(id, ownerUID string) pkgtimeline.TimelineEvent {
		event := activityEvent(id, "v1", "Pod", "pg-1", id, now)
		event.Owner = &pkgtimeline.OwnerInfo{APIVersion: Group + "/v1", Kind: "Cluster", Name: "pg", UID: ownerUID}
		return event
	}
	live := &unstructured.Unstructured{Object: map[string]any{"apiVersion": Group + "/v1", "kind": "Cluster", "metadata": map[string]any{"namespace": "db", "name": "pg", "uid": "new"}}}
	for _, failed := range []bool{false, true} {
		reader := activityReader()
		cache := &k8s.ResourceCache{}
		reader.Observations.Cache = cache
		reader.Observations.DynamicList = func(_ context.Context, received *k8s.ResourceCache, kind, group, namespace string) ([]*unstructured.Unstructured, error) {
			if received != cache || kind != "Cluster" || group != Group || namespace != "db" {
				t.Fatalf("live read escaped selected cache/identity")
			}
			if failed {
				return nil, errors.New("optional cache enrichment failed")
			}
			return []*unstructured.Unstructured{live}, nil
		}
		reader.Observations.TypedScope = func(context.Context, *k8s.ResourceCache, []string, string, string) (integration.KindAccess, []string) {
			return integration.KindAccess{State: integration.KindCoverageDenied}, nil
		}
		response, err := reader.ClusterActivity(context.Background(), &activityTimeline{rows: []pkgtimeline.TimelineEvent{row("old", "previous"), row("new", "new")}}, "ctx", "db", "pg", ActivityOptions{Since: now.Add(-time.Hour), Limit: 200})
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"new"}
		if failed {
			want = []string{"new", "old"}
		}
		if got := activityEventIDs(response); !reflect.DeepEqual(got, want) {
			t.Fatalf("enrichment failed=%v: events=%v want=%v", failed, got, want)
		}
	}
}

func TestClusterActivityQueryFailuresPreserveCauseAndResponseMessage(t *testing.T) {
	cause := errors.New("timeline unavailable")
	for _, floor := range []bool{false, true} {
		store := &activityTimeline{}
		if floor {
			store.oldestErr = cause
		} else {
			store.rowsErr = cause
		}
		response, err := activityReader().ClusterActivity(context.Background(), store, "ctx", "db", "pg", ActivityOptions{Limit: 200})
		var readErr *ActivityReadError
		if response != nil || !errors.Is(err, cause) || !errors.As(err, &readErr) || err.Error() != cause.Error() {
			t.Fatalf("error contract changed: response=%v err=%v", response, err)
		}
		want := "query activity"
		if floor {
			want = "query retention floor"
		}
		if readErr.Operation != want {
			t.Fatalf("operation = %q, want %q", readErr.Operation, want)
		}
	}
}

func TestCNPGRowAttributionUsesExactOwnerAndKindIdentity(t *testing.T) {
	labels := map[string]string{pkgtimeline.CNPGClusterLabel: "pg"}
	owner := &pkgtimeline.OwnerInfo{APIVersion: Group + "/v1", Kind: "Cluster", Name: "pg", UID: "current"}
	for _, test := range []struct {
		name, version, kind string
		owner               *pkgtimeline.OwnerInfo
		live                types.UID
		matched, labelled   bool
	}{
		{"owned instance", "v1", "Pod", owner, "current", true, false},
		{"previous instance", "v1", "Pod", owner, "recreated", false, false},
		{"uncontrolled labelled Pod", "v1", "Pod", nil, "", false, false},
		{"deleted Backup", Group + "/v1", "Backup", nil, "", true, true},
		{"Barman ObjectStore", barmanGroup + "/v1", "ObjectStore", nil, "", true, true},
		{"foreign Backup", "velero.io/v1", "Backup", nil, "", false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			event := &pkgtimeline.TimelineEvent{APIVersion: test.version, Kind: test.kind, Owner: test.owner, Labels: labels}
			matched, labelled := cnpgRowAttribution(event, "pg", string(test.live))
			if matched != test.matched || labelled != test.labelled {
				t.Fatalf("matched=%v labelled=%v", matched, labelled)
			}
		})
	}
}
