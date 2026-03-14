# CRD Integration Guide

Step-by-step checklist for adding first-class support for a new CRD (Custom Resource Definition) to Radar. Follow this when adding integrations like Knative, Istio, Karpenter, etc.

Study an existing integration that's similar to yours before starting. Good references:
- **Simple (no collisions):** Karpenter, KEDA, Velero
- **With topology:** FluxCD, ArgoCD
- **With kind collisions:** Istio (Gateway), Knative (Service, Ingress, Certificate)

---

## Pre-Implementation: Collision Check

**Before writing any code**, check if your CRD kind names collide with core K8s kinds or other CRD integrations.

Common collisions:
| Kind | Core K8s | Other CRDs |
|------|----------|------------|
| Service | `v1` | Knative `serving.knative.dev` |
| Ingress | `networking.k8s.io` | Knative `networking.internal.knative.dev` |
| Gateway | `gateway.networking.k8s.io` | Istio `networking.istio.io` |
| Certificate | cert-manager `cert-manager.io` | Knative `networking.internal.knative.dev` |
| Configuration | — | Knative `serving.knative.dev` |
| Route | — | Knative `serving.knative.dev`, OpenShift |
| Broker | — | Knative `eventing.knative.dev` |
| Channel | — | Knative `messaging.knative.dev` |
| Backup | Velero `velero.io` | CloudNativePG `cnpg.io` |

If your kind collides, you need group-qualified handling throughout (marked with  below).

---

## Checklist

### 1. Backend: CRD Warmup

**File:** `internal/k8s/dynamic_cache.go` — `WarmupCommonCRDs()`

Add your CRD kinds to the warmup list so they're cached immediately on startup.

- **No collision:** Add kind name to `commonCRDs` slice
- ** Collision:** Add group-qualified warmup after the generic loop:
  ```go
  if gvr, ok := discovery.GetGVRWithGroup("Service", "serving.knative.dev"); ok {
      gvrs = append(gvrs, gvr)
      log.Printf("Warming up CRD: Service (serving.knative.dev)")
  }
  ```

### 2. Backend: Topology Types

**File:** `pkg/topology/types.go`

Add `NodeKind` constants for each resource that will appear in topology:
```go
KindKnativeService NodeKind = "KnativeService"
```

- ** Collision:** Use a prefixed kind name (e.g., `KnativeService` not `Service`)

### 3. Backend: Topology Builder

**File:** `pkg/topology/builder.go` — `buildResourcesTopology()`

Add a new section that:
1. **Lists resources** from the dynamic cache (handle errors, don't discard with `_`)
2. **Creates nodes** with health derived from `status.conditions`
3. **Creates edges** between your resources and to/from core K8s resources
4. **Adds kinds to `processedKinds`** to prevent duplicate generic CRD nodes

Edge type semantics (choose carefully):
| Edge Type | Meaning | Example |
|-----------|---------|---------|
| `EdgeManages` | Owner relationship | Deployment → ReplicaSet |
| `EdgeExposes` | Network exposure (UI: "Services" group) | Service → Pod, Route → Revision |
| `EdgeConfigures` | Configuration | ConfigMap → Deployment, DestinationRule → Service |
| `EdgeUses` | Scaling relationship | HPA → Deployment |
| `EdgeProtects` | Policy protection | PDB → Deployment |

**Performance tip:** If you need the same resource list for both node creation and edge creation, store it in a slice during phase 1 and reuse in phase 2. Don't re-fetch.

**File:** `pkg/topology/relationships.go`

Add your kinds to `buildNodeID` and `normalizeKind` maps.
- ** Collision:** Use a unique ID prefix (e.g., `knativeservice/`, `istiogateway/`)

### 4. Frontend: Group Name Mapping

**File:** `packages/k8s-ui/src/utils/api-resources.ts`

Map API group to display group name:
```typescript
'serving.knative.dev': 'Knative',
'eventing.knative.dev': 'Knative',
```

### 5. Frontend: Resource Utils

**Create:** `packages/k8s-ui/src/components/resources/resource-utils-{integration}.ts`

Status extraction functions. Most CRDs use the standard `status.conditions` pattern:
```typescript
export function getMyResourceStatus(data: any): { text: string; color: string } {
  const conditions = data?.status?.conditions || []
  const ready = conditions.find((c: any) => c.type === 'Ready')
  // ...
}
```

### 6. Frontend: Table Columns

**File:** `packages/k8s-ui/src/components/resources/ResourcesView.tsx` — `KNOWN_COLUMNS`

Add column definitions. The key is the **lowercase plural** of the kind.

- ** Collision:** Add entry to `GROUP_QUALIFIED_COLUMN_KEYS`:
  ```typescript
  const GROUP_QUALIFIED_COLUMN_KEYS: Record<string, Record<string, string>> = {
    services: { 'serving.knative.dev': 'knativeservices' },
    ingresses: { 'networking.internal.knative.dev': 'knativeingresses' },
  }
  ```

Also verify `normalizeKindToPlural()` handles your kind correctly (watch out for kinds ending in 's', 'sh', 'ch', 'x', 'z').

### 7. Frontend: Cell Renderers

**Create:** `packages/k8s-ui/src/components/resources/renderers/{integration}-cells.tsx`

Cell components that render rich table cells (status badges, links, etc.).

**File:** `packages/k8s-ui/src/components/resources/ResourcesView.tsx` — `CellContent` component

Add cases for your kinds.
- ** Collision:** Use `apiVersion` checks:
  ```typescript
  if (kind === 'services' && resource.apiVersion?.includes('serving.knative.dev')) {
    return <KnativeServiceCell ... />
  }
  ```

### 8. Frontend: Detail Renderers

**Create:** `packages/k8s-ui/src/components/resources/renderers/{ResourceName}Renderer.tsx`

Follow existing patterns: `AlertBanner` for problems, `Section` components, `PropertyList`, `ConditionsSection`.

**File:** `packages/k8s-ui/src/components/resources/renderers/index.ts` — export new renderers

**File:** `packages/k8s-ui/src/components/shared/ResourceRendererDispatch.tsx`

Three wiring points (all must be updated):

#### a. `KNOWN_KINDS` set
Add your kinds so the dispatch shows the custom renderer instead of generic YAML.

#### b. Render lines
Add conditional render for each kind:
```tsx
{kind === 'nodepools' && <NodePoolRenderer data={data} />}
```

** COLLISION GUARD (critical):** If your kind collides with a core kind, you must ALSO guard the existing core renderer:
```tsx
// Guard core renderer — exclude when it's actually a Knative Service
{kind === 'services' && !data?.apiVersion?.includes('serving.knative.dev') && <ServiceRenderer ... />}

// Add Knative renderer with positive check
{(kind === 'services' && data?.apiVersion?.includes('serving.knative.dev')) && <KnativeServiceRenderer ... />}
```

This applies to the renderer AND to any action buttons (e.g., Port Forward button should not show for KNative Services).

#### c. `getResourceStatus()` function
Add your status function. ** COLLISION GUARD:** Insert apiVersion check INSIDE the existing kind's block, BEFORE the core status function:
```typescript
if (k === 'services') {
  if (data.apiVersion?.includes('serving.knative.dev')) {
    return getKnativeServiceStatus(data)  // Must come first!
  }
  return getServiceStatus(data)  // Core fallback
}
```

### 9. Frontend: Topology UI

Update these files to support new topology node kinds:

| File | What to Add |
|------|-------------|
| `packages/k8s-ui/src/types/core.ts` | Kind to `CoreNodeKind` type union + `displayKind` map |
| `web/src/App.tsx` | Kind to `ALL_NODE_KINDS` array |
| `packages/k8s-ui/src/utils/resource-icons.ts` | Icon mapping |
| `packages/k8s-ui/src/utils/badge-colors.ts` | Badge CSS class |
| `packages/k8s-ui/src/components/topology/TopologyFilterSidebar.tsx` | Filter sidebar entry |
| `packages/k8s-ui/src/components/topology/K8sResourceNode.tsx` | Node dimensions |
| `packages/k8s-ui/src/components/topology/layout.ts` | `kindPriority` entry |
| `packages/k8s-ui/src/utils/resource-hierarchy.ts` | `appLabelEligibleKinds` (if groupable by app label) |
| `packages/k8s-ui/src/components/topology/topology.css` | `.topology-icon-{kind}` CSS class with color |

### 10. Documentation

| File | What to Update |
|------|----------------|
| `docs/integrations.md` | Full integration section (follow existing pattern) |
| `README.md` | Add to "Supported Resources" table |
| `CLAUDE.md` | Add to renderers list and topology builder section |

---

## Verification Checklist

After implementation, verify:

- [ ] `npm run tsc` — no TypeScript errors
- [ ] `go test ./...` — no Go test failures
- [ ] `make build` — full build succeeds
- [ ] Resource table shows custom columns (not generic)
- [ ] Detail drawer shows custom renderer (not generic YAML)
- [ ] ** If collisions:** Core kind still renders correctly (e.g., core K8s Service still shows Ports/Selector, not KNative sections)
- [ ] ** If collisions:** `getResourceStatus()` returns correct status for both core and CRD kinds
- [ ] ** If collisions:** Action buttons (Port Forward, etc.) only show for appropriate kinds
- [ ] Topology shows nodes with correct icons and edges
- [ ] No regressions on existing resource types

---

## Common Gotchas

1. **Collision guards need THREE places:** renderer line, `getResourceStatus()`, AND action buttons. Missing any one causes bugs.

2. **`normalizeKindToPlural`** doesn't handle all pluralization. Kinds ending in 's' (like "Ingress") need the `+'es'` path. Test with your kind names.

3. **`processedKinds` exclusion** in builder.go prevents your resources from appearing twice (once as custom topology nodes, once as generic CRD nodes). If you add topology nodes, add the kind to `processedKinds`.

4. **Dynamic cache errors** from `dynamicCache.List()` should be logged, not discarded with `_`. Silent failures are hard to debug.

5. **Two-phase topology pattern:** Store resources in slices during node creation (phase 1), reuse during edge creation (phase 2). Don't call `dynamicCache.List()` twice for the same kind.

6. **Edge type choice matters:** Edge types drive the "Related Resources" grouping in the detail drawer. `EdgeExposes` creates a "Services" group, `EdgeManages` creates "Children", `EdgeConfigures` creates "Configuration", etc. Choose semantically, not by code convenience.

7. **CSS topology icon classes** must use the lowercase kind name: `.topology-icon-knativeservice`, not `.topology-icon-KnativeService`.
