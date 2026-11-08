package cachesource

import (
	"fmt"
	"runtime"
	"testing"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

func BenchmarkAgentOwnershipBatch(b *testing.B) {
	for _, scenario := range []string{"omitted-claims", "listed-members", "unreadable-managers", "shared-controller", "many-namespaces"} {
		b.Run(scenario, func(b *testing.B) {
			world := ownershiptest.NewBuilder()
			var rows, members []resourceid.Ref
			deployment := world.Add("apps", "Deployment", "shop", "web")
			for i := range 5000 {
				name := fmt.Sprint("row-", i)
				switch scenario {
				case "shared-controller", "many-namespaces":
					if scenario == "many-namespaces" {
						deployment = world.Add("apps", "Deployment", fmt.Sprint("ns-", i%100), name)
					}
					rows = append(rows, world.Add("", "Pod", deployment.Ref.Namespace, name, deployment.OwnerRef(true)).Ref)
				default:
					row := world.Add("", "ConfigMap", "shop", name)
					if scenario == "omitted-claims" {
						world.Meta(row, nil, map[string]string{"argocd.argoproj.io/tracking-id": "argocd_app:/ConfigMap:shop/" + name})
						members = append(members, world.Add("", "ConfigMap", "shop", "listed-"+name).Ref)
					}
					rows = append(rows, row.Ref)
					if scenario == "listed-members" {
						members = append(members, row.Ref)
					}
				}
			}
			if scenario == "omitted-claims" || scenario == "listed-members" {
				world.Manager(ownership.ArgoApplication, "argocd", "app", ownership.MembersRecorded, "shop", members...)
			}
			if scenario == "unreadable-managers" {
				for i := range 100 {
					world.Manager(ownership.ArgoApplication, "argocd", fmt.Sprint("app-", i), ownership.MembersUnreadable, "shop")
				}
			}
			cache := render(b, world.World())
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				b.StopTimer()
				runtime.GC()
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				b.StartTimer()
				session := begin(b, cache.source())
				view := ownership.NewView(session, ownership.Unrestricted())
				for _, row := range rows {
					if scenario == "shared-controller" || scenario == "many-namespaces" {
						view.ControllerChain(row)
					} else {
						view.Manager(row)
					}
				}
				b.StopTimer()
				runtime.GC()
				runtime.ReadMemStats(&after)
				runtime.KeepAlive(view)
				if after.HeapAlloc > before.HeapAlloc {
					b.ReportMetric(float64(after.HeapAlloc-before.HeapAlloc), "retained_B")
				}
				b.StartTimer()
				if err := session.ValidateCaches(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
