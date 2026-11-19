# Accepted drawer relationship differences

The family-based drawer answer changes 40 fields from the broadcast graph baseline. Ownership coverage, owner/owners/children/deployment and revision splitting are unchanged. These are the intended difference classes:

| Class | Example and reason |
|---|---|
| Empty policy selectors | `Deployment shop/web` gains `NetworkPolicy shop/default-deny`; the policy gains its selected workloads in `protects`. An empty NetworkPolicy selector selects every workload in its namespace. |
| UID-correct containment | `Deployment shop/api` loses `Pod shop/api-6b7-a`: the ReplicaSet names a replaced Deployment UID. `Pod shop/api-6b7-a` stops its `managedBy` climb at `ReplicaSet shop/api-6b7`, whose own `managedBy` becomes empty. |
| Complete Pod containment | `Deployment shop/big` gains all seven `big-8f9-*` Pods. Display Pod grouping no longer removes real owned Pods from the answer. |
| Highest readable controller | `ReplicaSet shop/web-7d9f8`, its Pods and `Widget shop/web-widget` name `Deployment shop/web` in `managedBy`, rather than the Application above a graph shortcut. The big Deployment's Pods now name their controller, and `Object /orders-db-config` names its XR instead of a claim graph ancestor. |
| Direct spec generator | `Node /node-a` names its Machine and `Node /node-b` its NodeClaim in `managedBy`, instead of graph ancestors Cluster and NodePool. |
| Single recording manager fallback | `Ingress shop/web` gains Application `argocd/shop` and Kustomization `flux-system/shop` gains Kustomization `flux-system/platform` in `managedBy` when no controller lead exists. |
| Gateway composition | `Service shop/web` and `Deployment shop/web` gain Gateway `shop/public`, following Gateway → HTTPRoute → Service → workload. These edges were present in the old graph but its flat query did not compose them. |
| Implementation selection is not management | Gateway `shop/public` loses GatewayClass `/envoy` from `managedBy`: a class selects an implementation; it is neither an owner nor a spec generator. |
| SealedSecret declaration join | `StatefulSet team/db` and `Pod team/db-0` gain SealedSecret `team/db-creds` in `configRefs`, and that SealedSecret gains both consumers. The join uses declared Secret coordinates without requiring the Secret to exist or be readable. |
| Hand-made Crossplane fixture without a backlink | XR `demo.example.io:XDatabase /orders-db-xr` loses claim `DatabaseClaim shop/orders-db` from `managers` and `managedBy`. Only the claim's `spec.resourceRef` points at the XR; the XR records neither `spec.claimRef` nor an owner reference to the claim. Crossplane writes `spec.claimRef` on every bound composite, and Crossplane v2 has no claims. This fixture-only shape is accepted; no reverse scan of arbitrary claim kinds is added. |
