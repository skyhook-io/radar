package audit

import (
	"log"
	"slices"
	"strings"

	"github.com/skyhook-io/radar/internal/k8s"
	bp "github.com/skyhook-io/radar/pkg/audit"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
)

type dynamicConfigRefHandler func(*unstructured.Unstructured) []bp.ConfigObjectRef

const certManagerDefaultClusterResourceNamespace = "cert-manager"

type dynamicConfigRefOptions struct {
	Scope           *ReadScope
	ServiceAccounts []*corev1.ServiceAccount
	Deployments     []*appsv1.Deployment
}

type dynamicConfigRefContext struct {
	serviceAccountImagePullSecrets    map[string][]string
	certManagerClusterResourceNSNames []string
}

func listDynamicConfigObjectRefs(namespaces []string, opts dynamicConfigRefOptions) []bp.ConfigObjectRef {
	cache := k8s.GetDynamicResourceCache()
	if cache == nil {
		return nil
	}
	ctx := newDynamicConfigRefContext(opts)

	nsSet := make(map[string]bool, len(namespaces))
	for _, ns := range namespaces {
		nsSet[ns] = true
	}

	var refs []bp.ConfigObjectRef
	seen := map[string]bool{}
	add := func(ref bp.ConfigObjectRef) {
		if ref.Kind != "ConfigMap" && ref.Kind != "Secret" {
			return
		}
		if ref.Namespace == "" || ref.Name == "" {
			return
		}
		if len(nsSet) > 0 && !nsSet[ref.Namespace] {
			return
		}
		key := ref.Kind + "\x00" + ref.Namespace + "\x00" + ref.Name
		if seen[key] {
			return
		}
		seen[key] = true
		refs = append(refs, ref)
	}

	for _, gvr := range cache.WatchedGVRs() {
		handler := dynamicConfigRefHandlerFor(gvr)
		if handler == nil {
			continue
		}
		items, err := cache.ListWatched(gvr)
		if err != nil {
			if !apierrors.IsForbidden(err) && !apierrors.IsUnauthorized(err) {
				log.Printf("[audit] CRD config-ref scan: skipping %s/%s: %v", gvr.GroupResource(), gvr.Version, err)
			}
			continue
		}
		handlerCanCrossNamespace := dynamicConfigRefHandlerCanCrossNamespace(gvr)
		for _, u := range items {
			if u == nil || !opts.Scope.allows(gvr, u.GetNamespace()) || !cache.IsNamespaceSynced(gvr, u.GetNamespace()) {
				continue
			}
			if len(nsSet) > 0 && !handlerCanCrossNamespace && u.GetNamespace() != "" && !nsSet[u.GetNamespace()] {
				continue
			}
			itemRefs := handler(u)
			if gvr.Group == "cert-manager.io" && gvr.Resource == "clusterissuers" {
				itemRefs = certManagerIssuerConfigRefsForNamespaces(u, ctx.certManagerClusterResourceNSNames)
			}
			itemRefs = append(itemRefs, dynamicConfigRefExtraRefs(gvr, u, ctx)...)
			for _, ref := range itemRefs {
				add(ref)
			}
		}
	}
	return refs
}

func newDynamicConfigRefContext(opts dynamicConfigRefOptions) dynamicConfigRefContext {
	return dynamicConfigRefContext{
		serviceAccountImagePullSecrets:    serviceAccountImagePullSecrets(opts.ServiceAccounts),
		certManagerClusterResourceNSNames: certManagerClusterResourceNamespaces(opts.Deployments),
	}
}

func serviceAccountImagePullSecrets(sas []*corev1.ServiceAccount) map[string][]string {
	out := map[string][]string{}
	for _, sa := range sas {
		if sa == nil || sa.Namespace == "" || sa.Name == "" || len(sa.ImagePullSecrets) == 0 {
			continue
		}
		key := sa.Namespace + "/" + sa.Name
		for _, ref := range sa.ImagePullSecrets {
			if ref.Name != "" {
				out[key] = append(out[key], ref.Name)
			}
		}
	}
	return out
}

type configRefDependency struct {
	extract        dynamicConfigRefHandler
	configMaps     bool
	crossNamespace bool
}

var configRefDependencies = map[schema.GroupResource]configRefDependency{
	{Group: "gateway.networking.k8s.io", Resource: "gateways"}:                {gatewayConfigRefs, false, true},
	{Group: "traefik.io", Resource: "ingressroutes"}:                          {traefikRouteConfigRefs, false, false},
	{Group: "traefik.io", Resource: "ingressroutetcps"}:                       {traefikRouteConfigRefs, false, false},
	{Group: "traefik.containo.us", Resource: "ingressroutes"}:                 {traefikRouteConfigRefs, false, false},
	{Group: "traefik.containo.us", Resource: "ingressroutetcps"}:              {traefikRouteConfigRefs, false, false},
	{Group: "traefik.io", Resource: "middlewares"}:                            {traefikMiddlewareConfigRefs, false, false},
	{Group: "traefik.containo.us", Resource: "middlewares"}:                   {traefikMiddlewareConfigRefs, false, false},
	{Group: "traefik.io", Resource: "serverstransports"}:                      {traefikTransportConfigRefs, false, false},
	{Group: "traefik.io", Resource: "serverstransporttcps"}:                   {traefikTransportConfigRefs, false, false},
	{Group: "traefik.containo.us", Resource: "serverstransports"}:             {traefikTransportConfigRefs, false, false},
	{Group: "traefik.containo.us", Resource: "serverstransporttcps"}:          {traefikTransportConfigRefs, false, false},
	{Group: "traefik.io", Resource: "tlsoptions"}:                             {traefikTLSOptionConfigRefs, false, false},
	{Group: "traefik.containo.us", Resource: "tlsoptions"}:                    {traefikTLSOptionConfigRefs, false, false},
	{Group: "traefik.io", Resource: "tlsstores"}:                              {traefikTLSStoreConfigRefs, false, false},
	{Group: "traefik.containo.us", Resource: "tlsstores"}:                     {traefikTLSStoreConfigRefs, false, false},
	{Group: "projectcontour.io", Resource: "httpproxies"}:                     {contourHTTPProxyConfigRefs, false, false},
	{Group: "networking.istio.io", Resource: "gateways"}:                      {istioGatewayConfigRefs, false, false},
	{Group: "cert-manager.io", Resource: "certificates"}:                      {certManagerCertificateConfigRefs, false, false},
	{Group: "cert-manager.io", Resource: "issuers"}:                           {certManagerIssuerConfigRefs, false, false},
	{Group: "cert-manager.io", Resource: "clusterissuers"}:                    {certManagerClusterIssuerConfigRefs, false, false},
	{Group: "source.toolkit.fluxcd.io", Resource: "gitrepositories"}:          {fluxGitRepositoryConfigRefs, false, false},
	{Group: "source.toolkit.fluxcd.io", Resource: "ocirepositories"}:          {fluxOCIRepositoryConfigRefs, false, false},
	{Group: "source.toolkit.fluxcd.io", Resource: "helmrepositories"}:         {fluxHelmRepositoryConfigRefs, false, false},
	{Group: "kustomize.toolkit.fluxcd.io", Resource: "kustomizations"}:        {fluxKustomizationConfigRefs, true, false},
	{Group: "helm.toolkit.fluxcd.io", Resource: "helmreleases"}:               {fluxHelmReleaseConfigRefs, true, false},
	{Group: "external-secrets.io", Resource: "externalsecrets"}:               {externalSecretConfigRefs, true, false},
	{Group: "keda.sh", Resource: "triggerauthentications"}:                    {kedaTriggerAuthConfigRefs, true, false},
	{Group: "monitoring.coreos.com", Resource: "servicemonitors"}:             {prometheusServiceMonitorConfigRefs, true, false},
	{Group: "monitoring.coreos.com", Resource: "podmonitors"}:                 {prometheusPodMonitorConfigRefs, true, false},
	{Group: "monitoring.coreos.com", Resource: "alertmanagers"}:               {alertmanagerConfigRefs, true, false},
	{Group: "kubernetes.crossplane.io", Resource: "providerconfigs"}:          {crossplaneProviderConfigRefs, false, true},
	{Group: "helm.crossplane.io", Resource: "providerconfigs"}:                {crossplaneProviderConfigRefs, false, true},
	{Group: "helm.crossplane.io", Resource: "releases"}:                       {crossplaneHelmReleaseConfigRefs, true, true},
	{Group: "argoproj.io", Resource: "rollouts"}:                              {rolloutConfigRefs, true, false},
	{Group: "serving.knative.dev", Resource: "services"}:                      {knativeTemplateConfigRefs, true, false},
	{Group: "serving.knative.dev", Resource: "configurations"}:                {knativeTemplateConfigRefs, true, false},
	{Group: "serving.knative.dev", Resource: "revisions"}:                     {knativeRevisionConfigRefs, true, false},
	{Group: "serving.knative.dev", Resource: "domainmappings"}:                {knativeDomainMappingConfigRefs, false, false},
	{Group: "sources.knative.dev", Resource: "containersources"}:              {knativeContainerSourceConfigRefs, true, false},
	{Group: "postgresql.cnpg.io", Resource: "clusters"}:                       {cnpgClusterConfigRefs, true, false},
	{Group: "postgresql.cnpg.io", Resource: "poolers"}:                        {cnpgPoolerConfigRefs, false, false},
	{Group: "bootstrap.cluster.x-k8s.io", Resource: "kubeadmconfigs"}:         {kubeadmConfigRefs, false, false},
	{Group: "bootstrap.cluster.x-k8s.io", Resource: "kubeadmconfigtemplates"}: {kubeadmConfigTemplateRefs, false, false},
	{Group: "velero.io", Resource: "backupstoragelocations"}:                  {veleroLocationConfigRefs, false, false},
	{Group: "velero.io", Resource: "volumesnapshotlocations"}:                 {veleroLocationConfigRefs, false, false},
}

func dynamicConfigRefHandlerFor(gvr schema.GroupVersionResource) dynamicConfigRefHandler {
	return configRefDependencies[gvr.GroupResource()].extract
}

func gatewayConfigRefs(u *unstructured.Unstructured) []bp.ConfigObjectRef {
	var refs []bp.ConfigObjectRef
	ns := u.GetNamespace()
	for _, listener := range mapsAt(u.Object, "spec", "listeners") {
		for _, ref := range mapsAt(listener, "tls", "certificateRefs") {
			kind := stringValue(ref["kind"])
			group := stringValue(ref["group"])
			if kind != "" && kind != "Secret" {
				continue
			}
			if group != "" && group != "core" {
				continue
			}
			refNS := stringValue(ref["namespace"])
			if refNS == "" {
				refNS = ns
			}
			addSecret(&refs, refNS, stringValue(ref["name"]))
		}
	}
	return refs
}

func traefikRouteConfigRefs(u *unstructured.Unstructured) []bp.ConfigObjectRef {
	var refs []bp.ConfigObjectRef
	addSecret(&refs, u.GetNamespace(), stringAt(u.Object, "spec", "tls", "secretName"))
	return refs
}

func traefikMiddlewareConfigRefs(u *unstructured.Unstructured) []bp.ConfigObjectRef {
	var refs []bp.ConfigObjectRef
	ns := u.GetNamespace()
	addSecret(&refs, ns, stringAt(u.Object, "spec", "basicAuth", "secret"))
	addSecret(&refs, ns, stringAt(u.Object, "spec", "digestAuth", "secret"))
	addSecret(&refs, ns, stringAt(u.Object, "spec", "forwardAuth", "tls", "caSecret"))
	return refs
}

func traefikTransportConfigRefs(u *unstructured.Unstructured) []bp.ConfigObjectRef {
	var refs []bp.ConfigObjectRef
	ns := u.GetNamespace()
	for _, name := range stringsAt(u.Object, "spec", "rootCAsSecrets") {
		addSecret(&refs, ns, name)
	}
	for _, name := range stringsAt(u.Object, "spec", "certificatesSecrets") {
		addSecret(&refs, ns, name)
	}
	return refs
}

func traefikTLSOptionConfigRefs(u *unstructured.Unstructured) []bp.ConfigObjectRef {
	var refs []bp.ConfigObjectRef
	for _, name := range stringsAt(u.Object, "spec", "clientAuth", "secretNames") {
		addSecret(&refs, u.GetNamespace(), name)
	}
	return refs
}

func traefikTLSStoreConfigRefs(u *unstructured.Unstructured) []bp.ConfigObjectRef {
	var refs []bp.ConfigObjectRef
	addSecret(&refs, u.GetNamespace(), stringAt(u.Object, "spec", "defaultCertificate", "secretName"))
	return refs
}

func contourHTTPProxyConfigRefs(u *unstructured.Unstructured) []bp.ConfigObjectRef {
	var refs []bp.ConfigObjectRef
	addSecret(&refs, u.GetNamespace(), stringAt(u.Object, "spec", "virtualhost", "tls", "secretName"))
	return refs
}

func istioGatewayConfigRefs(u *unstructured.Unstructured) []bp.ConfigObjectRef {
	var refs []bp.ConfigObjectRef
	ns := u.GetNamespace()
	for _, server := range mapsAt(u.Object, "spec", "servers") {
		tls, ok, _ := unstructured.NestedMap(server, "tls")
		if !ok {
			continue
		}
		addSecret(&refs, ns, stringAt(tls, "credentialName"))
		for _, name := range stringsAt(tls, "credentialNames") {
			addSecret(&refs, ns, name)
		}
	}
	return refs
}

func certManagerCertificateConfigRefs(u *unstructured.Unstructured) []bp.ConfigObjectRef {
	var refs []bp.ConfigObjectRef
	ns := u.GetNamespace()
	addSecret(&refs, ns, stringAt(u.Object, "spec", "secretName"))
	addSecret(&refs, ns, stringAt(u.Object, "spec", "keystores", "pkcs12", "passwordSecretRef", "name"))
	addSecret(&refs, ns, stringAt(u.Object, "spec", "keystores", "jks", "passwordSecretRef", "name"))
	return refs
}

func certManagerIssuerConfigRefs(u *unstructured.Unstructured) []bp.ConfigObjectRef {
	return certManagerIssuerConfigRefsForNamespaces(u, []string{u.GetNamespace()})
}

func certManagerClusterIssuerConfigRefs(u *unstructured.Unstructured) []bp.ConfigObjectRef {
	return certManagerIssuerConfigRefsForNamespaces(u, []string{certManagerDefaultClusterResourceNamespace})
}

func certManagerIssuerConfigRefsForNamespaces(u *unstructured.Unstructured, namespaces []string) []bp.ConfigObjectRef {
	var refs []bp.ConfigObjectRef
	for _, ns := range namespaces {
		addSecret(&refs, ns, stringAt(u.Object, "spec", "acme", "privateKeySecretRef", "name"))
		addCertManagerACMESolverRefs(&refs, ns, u.Object)
		for _, path := range [][]string{
			{"spec", "acme", "externalAccountBinding", "keySecretRef", "name"},
			{"spec", "ca", "secretName"},
			{"spec", "vault", "caBundleSecretRef", "name"},
			{"spec", "vault", "clientCertSecretRef", "name"},
			{"spec", "vault", "clientKeySecretRef", "name"},
			{"spec", "vault", "auth", "tokenSecretRef", "name"},
			{"spec", "vault", "auth", "appRole", "secretRef", "name"},
			{"spec", "vault", "auth", "kubernetes", "secretRef", "name"},
			{"spec", "vault", "auth", "clientCertificate", "secretName"},
			{"spec", "venafi", "tpp", "credentialsRef", "name"},
			{"spec", "venafi", "tpp", "caBundleSecretRef", "name"},
			{"spec", "venafi", "cloud", "apiTokenSecretRef", "name"},
			{"spec", "venafi", "ngts", "credentialsRef", "name"},
		} {
			addSecret(&refs, ns, stringAt(u.Object, path...))
		}
	}
	return refs
}

func fluxGitRepositoryConfigRefs(u *unstructured.Unstructured) []bp.ConfigObjectRef {
	var refs []bp.ConfigObjectRef
	ns := u.GetNamespace()
	addSecret(&refs, ns, stringAt(u.Object, "spec", "secretRef", "name"))
	addSecret(&refs, ns, stringAt(u.Object, "spec", "proxySecretRef", "name"))
	addSecret(&refs, ns, stringAt(u.Object, "spec", "verify", "secretRef", "name"))
	return refs
}

func fluxOCIRepositoryConfigRefs(u *unstructured.Unstructured) []bp.ConfigObjectRef {
	var refs []bp.ConfigObjectRef
	ns := u.GetNamespace()
	addSecret(&refs, ns, stringAt(u.Object, "spec", "secretRef", "name"))
	addSecret(&refs, ns, stringAt(u.Object, "spec", "certSecretRef", "name"))
	addSecret(&refs, ns, stringAt(u.Object, "spec", "proxySecretRef", "name"))
	addSecret(&refs, ns, stringAt(u.Object, "spec", "verify", "secretRef", "name"))
	return refs
}

func fluxHelmRepositoryConfigRefs(u *unstructured.Unstructured) []bp.ConfigObjectRef {
	var refs []bp.ConfigObjectRef
	ns := u.GetNamespace()
	addSecret(&refs, ns, stringAt(u.Object, "spec", "secretRef", "name"))
	addSecret(&refs, ns, stringAt(u.Object, "spec", "certSecretRef", "name"))
	return refs
}

func fluxKustomizationConfigRefs(u *unstructured.Unstructured) []bp.ConfigObjectRef {
	var refs []bp.ConfigObjectRef
	ns := u.GetNamespace()
	addSecret(&refs, ns, stringAt(u.Object, "spec", "decryption", "secretRef", "name"))
	addSecret(&refs, ns, stringAt(u.Object, "spec", "kubeConfig", "secretRef", "name"))
	addConfigMap(&refs, ns, stringAt(u.Object, "spec", "kubeConfig", "configMapRef", "name"))
	for _, ref := range mapsAt(u.Object, "spec", "postBuild", "substituteFrom") {
		switch stringValue(ref["kind"]) {
		case "ConfigMap":
			addConfigMap(&refs, ns, stringValue(ref["name"]))
		case "Secret":
			addSecret(&refs, ns, stringValue(ref["name"]))
		}
	}
	return refs
}

func fluxHelmReleaseConfigRefs(u *unstructured.Unstructured) []bp.ConfigObjectRef {
	var refs []bp.ConfigObjectRef
	ns := u.GetNamespace()
	addSecret(&refs, ns, stringAt(u.Object, "spec", "kubeConfig", "secretRef", "name"))
	addConfigMap(&refs, ns, stringAt(u.Object, "spec", "kubeConfig", "configMapRef", "name"))
	addSecret(&refs, ns, stringAt(u.Object, "spec", "chart", "spec", "verify", "secretRef", "name"))
	for _, ref := range mapsAt(u.Object, "spec", "valuesFrom") {
		kind := stringValue(ref["kind"])
		if kind == "" || kind == "ConfigMap" {
			addConfigMap(&refs, ns, stringValue(ref["name"]))
		} else if kind == "Secret" {
			addSecret(&refs, ns, stringValue(ref["name"]))
		}
	}
	return refs
}

func externalSecretConfigRefs(u *unstructured.Unstructured) []bp.ConfigObjectRef {
	var refs []bp.ConfigObjectRef
	ns := u.GetNamespace()
	targetName := stringAt(u.Object, "spec", "target", "name")
	if targetName == "" {
		targetName = u.GetName()
	}
	addSecret(&refs, ns, targetName)
	for _, item := range mapsAt(u.Object, "spec", "target", "template", "templateFrom") {
		addConfigMap(&refs, ns, stringAt(item, "configMap", "name"))
		addSecret(&refs, ns, stringAt(item, "secret", "name"))
	}
	return refs
}

func kedaTriggerAuthConfigRefs(u *unstructured.Unstructured) []bp.ConfigObjectRef {
	var refs []bp.ConfigObjectRef
	ns := u.GetNamespace()
	for _, ref := range mapsAt(u.Object, "spec", "secretTargetRef") {
		addSecret(&refs, ns, stringValue(ref["name"]))
	}
	for _, ref := range mapsAt(u.Object, "spec", "configMapTargetRef") {
		addConfigMap(&refs, ns, stringValue(ref["name"]))
	}
	addSecret(&refs, ns, stringAt(u.Object, "spec", "gcpSecretManager", "credentials", "clientSecret", "name"))
	return refs
}

func prometheusServiceMonitorConfigRefs(u *unstructured.Unstructured) []bp.ConfigObjectRef {
	return prometheusEndpointConfigRefs(u.GetNamespace(), mapsAt(u.Object, "spec", "endpoints"))
}

func prometheusPodMonitorConfigRefs(u *unstructured.Unstructured) []bp.ConfigObjectRef {
	return prometheusEndpointConfigRefs(u.GetNamespace(), mapsAt(u.Object, "spec", "podMetricsEndpoints"))
}

func prometheusEndpointConfigRefs(ns string, endpoints []map[string]any) []bp.ConfigObjectRef {
	var refs []bp.ConfigObjectRef
	for _, ep := range endpoints {
		addSecret(&refs, ns, stringAt(ep, "authorization", "credentials", "name"))
		addSecret(&refs, ns, stringAt(ep, "basicAuth", "username", "name"))
		addSecret(&refs, ns, stringAt(ep, "basicAuth", "password", "name"))
		addSecret(&refs, ns, stringAt(ep, "oauth2", "clientSecret", "name"))
		addSecret(&refs, ns, stringAt(ep, "bearerTokenSecret", "name"))
		addSecret(&refs, ns, stringAt(ep, "tlsConfig", "keySecret", "name"))
		addSecret(&refs, ns, stringAt(ep, "tlsConfig", "ca", "secret", "name"))
		addSecret(&refs, ns, stringAt(ep, "tlsConfig", "cert", "secret", "name"))
		addConfigMap(&refs, ns, stringAt(ep, "tlsConfig", "ca", "configMap", "name"))
		addConfigMap(&refs, ns, stringAt(ep, "tlsConfig", "cert", "configMap", "name"))
		if headers, ok, _ := unstructured.NestedMap(ep, "proxyConnectHeader"); ok {
			for _, raw := range headers {
				for _, item := range asSlice(raw) {
					if m, ok := item.(map[string]any); ok {
						addSecret(&refs, ns, stringValue(m["name"]))
					}
				}
			}
		}
	}
	return refs
}

func alertmanagerConfigRefs(u *unstructured.Unstructured) []bp.ConfigObjectRef {
	var refs []bp.ConfigObjectRef
	ns := u.GetNamespace()
	for _, name := range stringsAt(u.Object, "spec", "secrets") {
		addSecret(&refs, ns, name)
	}
	for _, name := range stringsAt(u.Object, "spec", "configMaps") {
		addConfigMap(&refs, ns, name)
	}
	for _, name := range stringsAt(u.Object, "spec", "imagePullSecrets") {
		addSecret(&refs, ns, name)
	}
	addSecret(&refs, ns, stringAt(u.Object, "spec", "configSecret"))
	return refs
}

func crossplaneProviderConfigRefs(u *unstructured.Unstructured) []bp.ConfigObjectRef {
	var refs []bp.ConfigObjectRef
	addSecret(&refs, stringAt(u.Object, "spec", "credentials", "secretRef", "namespace"), stringAt(u.Object, "spec", "credentials", "secretRef", "name"))
	addSecret(&refs, stringAt(u.Object, "spec", "identity", "secretRef", "namespace"), stringAt(u.Object, "spec", "identity", "secretRef", "name"))
	return refs
}

func crossplaneHelmReleaseConfigRefs(u *unstructured.Unstructured) []bp.ConfigObjectRef {
	var refs []bp.ConfigObjectRef
	ns := u.GetNamespace()
	addExplicitSecretWithDefault(&refs, ns, u.Object, "spec", "forProvider", "chart", "pullSecretRef")
	for _, ref := range mapsAt(u.Object, "spec", "forProvider", "valuesFrom") {
		addExplicitKeySelectorRefsWithDefault(&refs, ns, ref)
	}
	for _, ref := range mapsAt(u.Object, "spec", "forProvider", "patchesFrom") {
		addExplicitKeySelectorRefsWithDefault(&refs, ns, ref)
		if valueFrom, ok, _ := unstructured.NestedMap(ref, "valueFrom"); ok {
			addExplicitKeySelectorRefsWithDefault(&refs, ns, valueFrom)
		}
	}
	return refs
}

func rolloutConfigRefs(u *unstructured.Unstructured) []bp.ConfigObjectRef {
	spec, ok, _ := unstructured.NestedMap(u.Object, "spec", "template", "spec")
	if !ok {
		return nil
	}
	return podSpecLikeConfigRefs(u.GetNamespace(), spec)
}

func knativeTemplateConfigRefs(u *unstructured.Unstructured) []bp.ConfigObjectRef {
	spec, ok, _ := unstructured.NestedMap(u.Object, "spec", "template", "spec")
	if !ok {
		return nil
	}
	return podSpecLikeConfigRefs(u.GetNamespace(), spec)
}

func knativeRevisionConfigRefs(u *unstructured.Unstructured) []bp.ConfigObjectRef {
	spec, ok, _ := unstructured.NestedMap(u.Object, "spec")
	if !ok {
		return nil
	}
	return podSpecLikeConfigRefs(u.GetNamespace(), spec)
}

func knativeContainerSourceConfigRefs(u *unstructured.Unstructured) []bp.ConfigObjectRef {
	spec, ok, _ := unstructured.NestedMap(u.Object, "spec", "template", "spec")
	if !ok {
		return nil
	}
	return podSpecLikeConfigRefs(u.GetNamespace(), spec)
}

func knativeDomainMappingConfigRefs(u *unstructured.Unstructured) []bp.ConfigObjectRef {
	var refs []bp.ConfigObjectRef
	addSecret(&refs, u.GetNamespace(), stringAt(u.Object, "spec", "tls", "secretName"))
	return refs
}

func cnpgClusterConfigRefs(u *unstructured.Unstructured) []bp.ConfigObjectRef {
	var refs []bp.ConfigObjectRef
	ns := u.GetNamespace()
	for _, name := range stringsAt(u.Object, "spec", "monitoring", "customQueriesConfigMap") {
		addConfigMap(&refs, ns, name)
	}
	addSecret(&refs, ns, stringAt(u.Object, "spec", "bootstrap", "initdb", "secret", "name"))
	for _, path := range [][]string{
		{"spec", "bootstrap", "initdb", "postInitSQLRefs"},
		{"spec", "bootstrap", "initdb", "postInitApplicationSQLRefs"},
		{"spec", "bootstrap", "initdb", "postInitTemplateSQLRefs"},
	} {
		addCNPGSQLRefs(&refs, ns, u.Object, path...)
	}
	for _, field := range []string{"clientCASecret", "replicationTLSSecret", "serverCASecret", "serverTLSSecret"} {
		addSecret(&refs, ns, nameOrStringAt(u.Object, "spec", "certificates", field))
	}
	addSecret(&refs, ns, stringAt(u.Object, "spec", "superuserSecret", "name"))
	return refs
}

func cnpgPoolerConfigRefs(u *unstructured.Unstructured) []bp.ConfigObjectRef {
	var refs []bp.ConfigObjectRef
	addSecret(&refs, u.GetNamespace(), stringAt(u.Object, "spec", "pgbouncer", "authQuerySecret", "name"))
	return refs
}

func kubeadmConfigRefs(u *unstructured.Unstructured) []bp.ConfigObjectRef {
	return kubeadmFileRefs(u.GetNamespace(), mapsAt(u.Object, "spec", "files"))
}

func kubeadmConfigTemplateRefs(u *unstructured.Unstructured) []bp.ConfigObjectRef {
	return kubeadmFileRefs(u.GetNamespace(), mapsAt(u.Object, "spec", "template", "spec", "files"))
}

func kubeadmFileRefs(ns string, files []map[string]any) []bp.ConfigObjectRef {
	var refs []bp.ConfigObjectRef
	for _, file := range files {
		addSecret(&refs, ns, stringAt(file, "contentFrom", "secret", "name"))
	}
	return refs
}

func veleroLocationConfigRefs(u *unstructured.Unstructured) []bp.ConfigObjectRef {
	var refs []bp.ConfigObjectRef
	ns := u.GetNamespace()
	addSecret(&refs, ns, stringAt(u.Object, "spec", "credential", "name"))
	addSecret(&refs, ns, stringAt(u.Object, "spec", "objectStorage", "caCertRef", "name"))
	return refs
}

func podSpecLikeConfigRefs(ns string, spec map[string]any) []bp.ConfigObjectRef {
	var refs []bp.ConfigObjectRef
	for _, field := range []string{"initContainers", "containers", "ephemeralContainers"} {
		for _, c := range mapsAt(spec, field) {
			collectContainerLikeRefs(&refs, ns, c)
		}
	}
	for _, v := range mapsAt(spec, "volumes") {
		collectVolumeLikeRefs(&refs, ns, v)
	}
	for _, name := range stringsAt(spec, "imagePullSecrets") {
		addSecret(&refs, ns, name)
	}
	return refs
}

func dynamicConfigRefExtraRefs(gvr schema.GroupVersionResource, u *unstructured.Unstructured, ctx dynamicConfigRefContext) []bp.ConfigObjectRef {
	spec, ok := dynamicPodSpecFor(gvr, u)
	if !ok {
		return nil
	}
	return podSpecLikeServiceAccountImagePullRefs(u.GetNamespace(), spec, ctx.serviceAccountImagePullSecrets)
}

func dynamicPodSpecFor(gvr schema.GroupVersionResource, u *unstructured.Unstructured) (map[string]any, bool) {
	switch gvr.Group {
	case "argoproj.io":
		if gvr.Resource == "rollouts" {
			spec, ok, _ := unstructured.NestedMap(u.Object, "spec", "template", "spec")
			return spec, ok
		}
	case "serving.knative.dev":
		switch gvr.Resource {
		case "services", "configurations":
			spec, ok, _ := unstructured.NestedMap(u.Object, "spec", "template", "spec")
			return spec, ok
		case "revisions":
			spec, ok, _ := unstructured.NestedMap(u.Object, "spec")
			return spec, ok
		}
	case "sources.knative.dev":
		if gvr.Resource == "containersources" {
			spec, ok, _ := unstructured.NestedMap(u.Object, "spec", "template", "spec")
			return spec, ok
		}
	}
	return nil, false
}

func podSpecLikeServiceAccountImagePullRefs(ns string, spec map[string]any, saImagePullSecrets map[string][]string) []bp.ConfigObjectRef {
	if len(saImagePullSecrets) == 0 || len(stringsAt(spec, "imagePullSecrets")) > 0 {
		return nil
	}
	saName := stringAt(spec, "serviceAccountName")
	if saName == "" {
		saName = "default"
	}
	var refs []bp.ConfigObjectRef
	for _, name := range saImagePullSecrets[ns+"/"+saName] {
		addSecret(&refs, ns, name)
	}
	return refs
}

func collectContainerLikeRefs(refs *[]bp.ConfigObjectRef, ns string, c map[string]any) {
	for _, env := range mapsAt(c, "env") {
		addConfigMap(refs, ns, stringAt(env, "valueFrom", "configMapKeyRef", "name"))
		addSecret(refs, ns, stringAt(env, "valueFrom", "secretKeyRef", "name"))
	}
	for _, envFrom := range mapsAt(c, "envFrom") {
		addConfigMap(refs, ns, stringAt(envFrom, "configMapRef", "name"))
		addSecret(refs, ns, stringAt(envFrom, "secretRef", "name"))
	}
}

func dynamicConfigRefHandlerCanCrossNamespace(gvr schema.GroupVersionResource) bool {
	switch gvr.Group {
	case "gateway.networking.k8s.io":
		return gvr.Resource == "gateways"
	case "kubernetes.crossplane.io", "helm.crossplane.io":
		return gvr.Resource == "providerconfigs" || gvr.Resource == "releases"
	}
	return false
}

func collectVolumeLikeRefs(refs *[]bp.ConfigObjectRef, ns string, v map[string]any) {
	addConfigMap(refs, ns, stringAt(v, "configMap", "name"))
	addSecret(refs, ns, stringAt(v, "secret", "secretName"))
	for _, src := range mapsAt(v, "projected", "sources") {
		addConfigMap(refs, ns, stringAt(src, "configMap", "name"))
		addSecret(refs, ns, stringAt(src, "secret", "name"))
	}
	addSecret(refs, ns, stringAt(v, "csi", "nodePublishSecretRef", "name"))
	addSecret(refs, ns, stringAt(v, "flexVolume", "secretRef", "name"))
	addSecret(refs, ns, stringAt(v, "azureFile", "secretName"))
	addSecret(refs, ns, stringAt(v, "cephfs", "secretRef", "name"))
	addSecret(refs, ns, stringAt(v, "rbd", "secretRef", "name"))
	addSecret(refs, ns, stringAt(v, "cinder", "secretRef", "name"))
	addSecret(refs, ns, stringAt(v, "scaleIO", "secretRef", "name"))
	addSecret(refs, ns, stringAt(v, "iscsi", "secretRef", "name"))
	addSecret(refs, ns, stringAt(v, "storageos", "secretRef", "name"))
}

func addCNPGSQLRefs(refs *[]bp.ConfigObjectRef, ns string, obj map[string]any, path ...string) {
	parent, ok, _ := unstructured.NestedMap(obj, path...)
	if !ok {
		return
	}
	for _, ref := range mapsAt(parent, "configMapRefs") {
		addConfigMap(refs, ns, stringValue(ref["name"]))
	}
	for _, ref := range mapsAt(parent, "secretRefs") {
		addSecret(refs, ns, stringValue(ref["name"]))
	}
}

func addExplicitKeySelectorRefs(refs *[]bp.ConfigObjectRef, obj map[string]any) {
	addExplicitConfigMap(refs, obj, "configMapKeyRef")
	addExplicitSecret(refs, obj, "secretKeyRef")
}

func addCertManagerACMESolverRefs(refs *[]bp.ConfigObjectRef, ns string, obj map[string]any) {
	for _, solver := range mapsAt(obj, "spec", "acme", "solvers") {
		dns01, ok, _ := unstructured.NestedMap(solver, "dns01")
		if !ok {
			continue
		}
		collectCertManagerSecretRefs(refs, ns, dns01)
	}
}

func collectCertManagerSecretRefs(refs *[]bp.ConfigObjectRef, ns string, obj any) {
	switch v := obj.(type) {
	case map[string]any:
		for key, child := range v {
			if strings.HasSuffix(key, "SecretRef") {
				if ref, ok := child.(map[string]any); ok {
					addSecret(refs, ns, stringValue(ref["name"]))
				}
			}
			collectCertManagerSecretRefs(refs, ns, child)
		}
	case []any:
		for _, child := range v {
			collectCertManagerSecretRefs(refs, ns, child)
		}
	}
}

func certManagerClusterResourceNamespaces(deployments []*appsv1.Deployment) []string {
	seen := map[string]bool{}
	var namespaces []string
	add := func(ns string) {
		if ns == "" || seen[ns] {
			return
		}
		seen[ns] = true
		namespaces = append(namespaces, ns)
	}
	for _, deploy := range deployments {
		if !isCertManagerDeployment(deploy) {
			continue
		}
		var ns string
		for _, c := range deploy.Spec.Template.Spec.Containers {
			args := slices.Concat(c.Command, c.Args)
			for _, arg := range args {
				if arg == "--config" || strings.HasPrefix(arg, "--config=") {
					return nil
				}
			}
			argNS := clusterResourceNamespaceArg(args)
			if argNS == "" {
				continue
			}
			ns = argNS
			if strings.HasPrefix(ns, "$(") && strings.HasSuffix(ns, ")") {
				name := ns[2 : len(ns)-1]
				ns = ""
				for _, env := range c.Env {
					if env.Name != name {
						continue
					}
					ns = ""
					if env.ValueFrom == nil {
						ns = env.Value
					} else if env.ValueFrom.FieldRef != nil && env.ValueFrom.FieldRef.FieldPath == "metadata.namespace" {
						ns = deploy.Namespace
					}
				}
			}
			break
		}
		if ns == "" || len(validation.IsDNS1123Label(ns)) != 0 {
			return nil
		}
		add(ns)
	}
	return namespaces
}

func isCertManagerDeployment(deploy *appsv1.Deployment) bool {
	if deploy == nil {
		return false
	}
	return deploy.Labels["app.kubernetes.io/name"] == "cert-manager" || deploy.Labels["app"] == "cert-manager"
}

func clusterResourceNamespaceArg(args []string) string {
	var namespace string
	for i, arg := range args {
		if strings.HasPrefix(arg, "--cluster-resource-namespace=") {
			namespace = strings.TrimSpace(strings.TrimPrefix(arg, "--cluster-resource-namespace="))
		}
		if arg == "--cluster-resource-namespace" && i+1 < len(args) {
			namespace = strings.TrimSpace(args[i+1])
		}
	}
	return namespace
}

func addExplicitConfigMap(refs *[]bp.ConfigObjectRef, obj map[string]any, path ...string) {
	ns := stringAt(obj, append(path, "namespace")...)
	name := stringAt(obj, append(path, "name")...)
	addConfigMap(refs, ns, name)
}

func addExplicitSecret(refs *[]bp.ConfigObjectRef, obj map[string]any, path ...string) {
	ns := stringAt(obj, append(path, "namespace")...)
	name := stringAt(obj, append(path, "name")...)
	addSecret(refs, ns, name)
}

func addExplicitKeySelectorRefsWithDefault(refs *[]bp.ConfigObjectRef, defaultNS string, obj map[string]any) {
	addExplicitConfigMapWithDefault(refs, defaultNS, obj, "configMapKeyRef")
	addExplicitSecretWithDefault(refs, defaultNS, obj, "secretKeyRef")
}

func addExplicitConfigMapWithDefault(refs *[]bp.ConfigObjectRef, defaultNS string, obj map[string]any, path ...string) {
	ns := stringAt(obj, append(path, "namespace")...)
	if ns == "" {
		ns = defaultNS
	}
	name := stringAt(obj, append(path, "name")...)
	addConfigMap(refs, ns, name)
}

func addExplicitSecretWithDefault(refs *[]bp.ConfigObjectRef, defaultNS string, obj map[string]any, path ...string) {
	ns := stringAt(obj, append(path, "namespace")...)
	if ns == "" {
		ns = defaultNS
	}
	name := stringAt(obj, append(path, "name")...)
	addSecret(refs, ns, name)
}

func addConfigMap(refs *[]bp.ConfigObjectRef, ns, name string) {
	if ns == "" || name == "" {
		return
	}
	*refs = append(*refs, bp.ConfigObjectRef{Kind: "ConfigMap", Namespace: ns, Name: name})
}

func addSecret(refs *[]bp.ConfigObjectRef, ns, name string) {
	if ns == "" || name == "" {
		return
	}
	*refs = append(*refs, bp.ConfigObjectRef{Kind: "Secret", Namespace: ns, Name: name})
}

func stringAt(obj map[string]any, fields ...string) string {
	v, ok, _ := unstructured.NestedString(obj, fields...)
	if ok {
		return v
	}
	return ""
}

func nameOrStringAt(obj map[string]any, fields ...string) string {
	if v := stringAt(obj, fields...); v != "" {
		return v
	}
	return stringAt(obj, append(fields, "name")...)
}

func mapsAt(obj map[string]any, fields ...string) []map[string]any {
	raw, ok, _ := unstructured.NestedSlice(obj, fields...)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func stringsAt(obj map[string]any, fields ...string) []string {
	raw, ok, _ := unstructured.NestedSlice(obj, fields...)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		switch v := item.(type) {
		case string:
			out = append(out, v)
		case map[string]any:
			if name := stringValue(v["name"]); name != "" {
				out = append(out, name)
			}
		}
	}
	return out
}

func asSlice(v any) []any {
	switch t := v.(type) {
	case []any:
		return t
	default:
		return nil
	}
}

func stringValue(v any) string {
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}
