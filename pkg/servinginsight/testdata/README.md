# RayService serving fixture

`rayservice-rollback.yaml` is a synthetic projection fixture using the KubeRay
[v1.7.0 API](https://github.com/ray-project/kuberay/blob/v1.7.0/ray-operator/apis/ray/v1/rayservice_types.go)
and [controller](https://github.com/ray-project/kuberay/blob/v1.7.0/ray-operator/controllers/ray/rayservice_controller.go).
It combines independent Ready/upgrade/rollback conditions with embedded runtime
conditions, RUNNING/DEPLOY_FAILED applications, and explicit zero traffic/capacity.
Embedded runtime conditions must not be projected as current health. It does not claim a controller
produced this exact snapshot or prove incremental Gateway traffic shifting.

The real-controller lane is `scripts/kuberay-demo.sh`: a healthy active Serve
application and an intentionally failing pending cluster. In that scenario,
KubeRay exposes the pending name but no embedded failure condition. The adapter
must retain that name without inferring child or application health.
