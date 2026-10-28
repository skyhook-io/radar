package k8s

import (
	"log"
	"strings"

	"github.com/skyhook-io/radar/internal/logsafe"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const gatewayAPIGroup = "gateway.networking.k8s.io"

// AWS's Gateway controllers take listener certificates from ACM and ignore
// certificateRefs.
var gatewayControllersIgnoringCertificateRefs = map[string]bool{
	"gateway.k8s.aws/alb": true,
	"gateway.k8s.aws/nlb": true,
	"application-networking.k8s.aws/gateway-api-controller": true,
}

// addGatewayTLSUses adds to idx the Secrets live Gateway listeners terminate
// TLS with, and every Secret a TLS listener references, under the listener
// hostname ("" for none, which serves every name the certificate covers).
//
// A Gateway counts as live when it is Programmed for its current generation
// and has an address; a listener counts when it terminates TLS (HTTPS, or TLS
// in Terminate mode; Passthrough ignores certificateRefs), is Programmed for
// the current generation, not rejected, conflicted or unresolved, and has
// routes attached. A listener with several certificateRefs gives no use: the
// controller picks one per client by hostname and key type, so any one of
// them may never be served. A cross-namespace Secret counts only when a
// ReferenceGrant allows it, as the controller would otherwise refuse it. The
// Gateway's class must resolve to a controller that serves certificateRefs
// (not AWS's, which take certificates from ACM). A Gateway or listener that
// names certificates held outside the cluster is skipped, as its Secret may
// not be what clients see. Listeners attached through ListenerSets are not
// read.
func addGatewayTLSUses(idx *tlsServingIndex) {
	dynamicCache := GetDynamicResourceCache()
	discovery := GetResourceDiscovery()
	if dynamicCache == nil || discovery == nil {
		return
	}
	gvr, ok := discovery.GetGVRWithGroup("Gateway", gatewayAPIGroup)
	if !ok {
		return
	}
	// Gateways in namespaces Radar doesn't see could hold the valid
	// competitor that makes another certificate unserved.
	if !dynamicCache.IsClusterWideSynced(gvr) {
		return
	}
	gateways, err := dynamicCache.ListWatched(gvr)
	if err != nil {
		log.Printf("[issues] failed to list Gateways for TLS certificate checks: %s", logsafe.Sanitize(err.Error()))
		return
	}
	classGVR, ok := discovery.GetGVRWithGroup("GatewayClass", gatewayAPIGroup)
	if !ok {
		return
	}
	if !dynamicCache.IsClusterWideSynced(classGVR) {
		return
	}
	classes, err := dynamicCache.ListWatched(classGVR)
	if err != nil {
		return
	}
	controllers := map[string]string{}
	for _, gc := range classes {
		controllers[gc.GetName()], _, _ = unstructured.NestedString(gc.Object, "spec", "controllerName")
	}
	getReferenceGrants := gatewayReferenceGrantGetter(dynamicCache, discovery)

	for _, gw := range gateways {
		if gw.GetDeletionTimestamp() != nil || strings.TrimSpace(gw.GetAnnotations()[gkeCertMapAnnotation]) != "" {
			continue
		}
		className, _, _ := unstructured.NestedString(gw.Object, "spec", "gatewayClassName")
		controller, ok := controllers[className]
		if !ok || controller == "" || gatewayControllersIgnoringCertificateRefs[controller] {
			continue
		}
		key := gatewayCertificatePool(controller)
		live := gatewayLive(gw)
		gwRef := issuesapi.Ref{Group: gatewayAPIGroup, Kind: "Gateway", Namespace: gw.GetNamespace(), Name: gw.GetName()}
		listenerStatus := gatewayListenerStatuses(gw)
		listeners, _, _ := unstructured.NestedSlice(gw.Object, "spec", "listeners")
		for _, raw := range listeners {
			listener, ok := raw.(map[string]any)
			if !ok || !listenerTerminatesTLS(listener) || listenerExternalCertificates(listener) {
				continue
			}
			hostname, _ := listener["hostname"].(string)
			hostname = normalizeTLSHost(hostname)
			refs, _, _ := unstructured.NestedSlice(listener, "tls", "certificateRefs")
			var secrets [][2]string
			for _, rawRef := range refs {
				ref, ok := rawRef.(map[string]any)
				if !ok {
					continue
				}
				group, _ := ref["group"].(string)
				kind, _ := ref["kind"].(string)
				secretName, _ := ref["name"].(string)
				secretNamespace, _ := ref["namespace"].(string)
				if group != "" || (kind != "" && kind != "Secret") || secretName == "" {
					continue
				}
				if secretNamespace == "" {
					secretNamespace = gw.GetNamespace()
				}
				if secretNamespace != gw.GetNamespace() && !gatewaySecretGranted(getReferenceGrants, gw.GetNamespace(), secretNamespace, secretName) {
					continue
				}
				idx.reference(key, secretNamespace, secretName, hostname)
				secrets = append(secrets, [2]string{secretNamespace, secretName})
			}
			name, _ := listener["name"].(string)
			status, hasStatus := listenerStatus[name]
			if !live || len(refs) != 1 || len(secrets) != 1 || !hasStatus || !listenerLive(status, gw.GetGeneration()) {
				continue
			}
			idx.uses = append(idx.uses, tlsUse{
				secretNamespace: secrets[0][0], secretName: secrets[0][1], host: hostname,
				controller: key, ref: gwRef, addressed: true,
			})
		}
	}
}

// gatewayCertificatePool names the certificate pool a Gateway controller
// serves from. Traefik loads Ingress and Gateway certificates into one store,
// so its Gateways share the Ingress controller's pool.
func gatewayCertificatePool(controller string) string {
	if controller == traefikGatewayController {
		return traefikIngressController
	}
	return "gateway:" + controller
}

const (
	traefikGatewayController = "traefik.io/gateway-controller"
	traefikIngressController = "traefik.io/ingress-controller"
)

// GKE takes certificates from a Certificate Manager map named on the Gateway,
// and GKE and the AWS Gateway API Controller take them from pre-shared or ACM
// certificates named in a listener's tls.options, alongside or instead of
// certificateRefs.
const gkeCertMapAnnotation = "networking.gke.io/certmap"

var gatewayExternalCertificateOptions = []string{
	"networking.gke.io/pre-shared-certs",
	"application-networking.k8s.aws/certificate-arn",
}

func listenerExternalCertificates(listener map[string]any) bool {
	options, _, _ := unstructured.NestedStringMap(listener, "tls", "options")
	for _, key := range gatewayExternalCertificateOptions {
		if strings.TrimSpace(options[key]) != "" {
			return true
		}
	}
	return false
}

func gatewayLive(gw *unstructured.Unstructured) bool {
	addresses, _, _ := unstructured.NestedSlice(gw.Object, "status", "addresses")
	hasAddress := false
	for _, raw := range addresses {
		if a, ok := raw.(map[string]any); ok {
			if value, _ := a["value"].(string); value != "" {
				hasAddress = true
			}
		}
	}
	if !hasAddress {
		return false
	}
	conds, _, _ := unstructured.NestedSlice(gw.Object, "status", "conditions")
	for _, raw := range conds {
		c, ok := raw.(map[string]any)
		if !ok || c["type"] != "Programmed" {
			continue
		}
		return c["status"] == "True" && int64Field(c["observedGeneration"]) == gw.GetGeneration()
	}
	return false
}

func gatewayListenerStatuses(gw *unstructured.Unstructured) map[string]map[string]any {
	out := map[string]map[string]any{}
	statuses, _, _ := unstructured.NestedSlice(gw.Object, "status", "listeners")
	for _, raw := range statuses {
		if s, ok := raw.(map[string]any); ok {
			if name, _ := s["name"].(string); name != "" {
				out[name] = s
			}
		}
	}
	return out
}

func listenerTerminatesTLS(listener map[string]any) bool {
	protocol, _ := listener["protocol"].(string)
	mode, _, _ := unstructured.NestedString(listener, "tls", "mode")
	switch strings.ToUpper(protocol) {
	case "HTTPS":
		return true
	case "TLS":
		return mode == "" || mode == "Terminate"
	}
	return false
}

func listenerLive(status map[string]any, generation int64) bool {
	if int64Field(status["attachedRoutes"]) <= 0 {
		return false
	}
	programmed := false
	conds, _ := status["conditions"].([]any)
	for _, raw := range conds {
		c, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch c["type"] {
		case "Programmed":
			programmed = c["status"] == "True" && int64Field(c["observedGeneration"]) == generation
		case "Accepted", "ResolvedRefs":
			if c["status"] == "False" {
				return false
			}
		case "Conflicted":
			if c["status"] == "True" {
				return false
			}
		}
	}
	return programmed
}

func gatewaySecretGranted(getReferenceGrants referenceGrantGetter, gatewayNamespace, secretNamespace, secretName string) bool {
	if getReferenceGrants == nil {
		return false
	}
	grants, ok := getReferenceGrants(secretNamespace)
	if !ok {
		return false
	}
	for _, grant := range grants {
		froms, foundFrom, _ := unstructured.NestedSlice(grant.Object, "spec", "from")
		tos, foundTo, _ := unstructured.NestedSlice(grant.Object, "spec", "to")
		if !foundFrom || !foundTo || !referenceGrantAllowsFrom(froms, "Gateway", gatewayNamespace) {
			continue
		}
		for _, raw := range tos {
			to, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			group, _ := to["group"].(string)
			kind, _ := to["kind"].(string)
			name, _ := to["name"].(string)
			if group == "" && kind == "Secret" && (name == "" || name == secretName) {
				return true
			}
		}
	}
	return false
}

func int64Field(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int32:
		return int64(n)
	case int:
		return int64(n)
	case float64:
		return int64(n)
	}
	return 0
}
