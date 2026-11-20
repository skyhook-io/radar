# KServe InferenceService serving fixtures

The `*.json` files are real `serving.kserve.io/v1beta1` InferenceServices written by
the [KServe v0.20.0](https://github.com/kserve/kserve/tree/v0.20.0) controller in
Standard mode (sklearn iris model, one predictor replica). Only names were changed
(namespace, missing storage path, image tag, runtime and model format) and metadata
was trimmed to name, namespace, generation and annotations. URLs, storage URIs and
failure messages are kept so tests can check that the projection drops them.

| File | How the controller got there | What it shows |
|---|---|---|
| `loaded.json` | fresh model load | UpToDate / Loaded |
| `failed-after-loaded.json` | Loaded, then `storageUri` changed to a missing object; Recreate strategy removed the old Pod | BlockedByFailedLoad while `activeModelState` still reads Loaded and nothing serves |
| `recovered-stale-failure.json` | the previous state, then `storageUri` restored | Loaded with the earlier `lastFailureInfo` kept |
| `imagepull-after-older-failure.json` | Loaded with a stale failure, then `runtimeVersion` set to a missing image tag | InProgress / Pending; the recorded failure is the older storage error, not the image pull |
| `uptodate-while-new-pod-unscheduled.json` | RollingUpdate to a missing object while the cluster had no room for the new Pod | UpToDate / Loaded reported from Deployment availability while the old Pod serves the old model |
| `canary-standard.json` | `spec.canary` with two canaries whose Pods could not be scheduled | canary `ready:false`, declared `trafficPercent`, canary `modelStatus` that v0.20.0 never fills |
| `stopped.json` | `serving.kserve.io/stop: "true"` | model state and failure history cleared |
| `invalid-spec.json` | model format no runtime supports | InvalidSpec / NoSupportingRuntime without an exit code |
| `transformer-not-ready.json` | predictor plus a transformer whose Pod could not be scheduled | Ready True while TransformerReady is False; no component revision facts in Standard mode |
| `namespaced-runtime.json` | namespaced ServingRuntime | `servingRuntimeName` instead of `clusterServingRuntimeName` |

`knative-canary-failed-revision.synthetic.yaml` is not a capture: no Knative-mode
KServe controller was available. It follows the v0.20.0 Knative writers
(`PropagateStatus` in `pkg/apis/serving/v1beta1/inference_service_status.go` and the
`latest`/`prev` traffic targets in `ksvc_reconciler.go`) for a `canaryTrafficPercent`
rollout whose new revision failed to load.
