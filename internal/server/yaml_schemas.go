package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/openapi"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/auth"
)

const maxYAMLSchemaRequestBytes = 256 << 10
const maxYAMLSchemaDocuments = 64
const maxYAMLSchemaResponseBytes = 8 << 20
const maxYAMLSchemaSourceBytes = 32 << 20
const maxYAMLSchemaCacheBytes = 64 << 20
const maxYAMLSchemaCacheEntries = 128
const yamlSchemaPathCacheTTL = 30 * time.Second

const deprecatedSchemaAdvisory = "[radar-advisory:deprecated] This field is deprecated."

type yamlSchemaIdentity struct {
	Index      int    `json:"index"`
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
}

type yamlSchemaRequest struct {
	Documents []yamlSchemaIdentity `json:"documents"`
}

type yamlSchemaDocument struct {
	Index     int    `json:"index"`
	Status    string `json:"status"`
	BundleKey string `json:"bundleKey,omitempty"`
	SchemaRef string `json:"schemaRef,omitempty"`
	Error     string `json:"error,omitempty"`
}

type yamlSchemaBundle struct {
	Definitions map[string]any `json:"definitions"`
}

type yamlSchemaResponse struct {
	Documents []yamlSchemaDocument        `json:"documents"`
	Bundles   map[string]yamlSchemaBundle `json:"bundles"`
}

type requestedSchemaGroup struct {
	path      string
	documents []yamlSchemaIdentity
}

type yamlSchemaPathCacheEntry struct {
	paths     map[string]openapi.GroupVersion
	expiresAt time.Time
}

type yamlSchemaBundleCacheEntry struct {
	bundle yamlSchemaBundle
	roots  map[string]string
	size   int
}

func (s *Server) handleResourceSchemas(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}

	var req yamlSchemaRequest
	if err := decodeBoundedJSONBody(w, r, maxYAMLSchemaRequestBytes, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid schema request: "+err.Error())
		return
	}
	if len(req.Documents) == 0 || len(req.Documents) > maxYAMLSchemaDocuments {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("schema request must contain 1-%d documents", maxYAMLSchemaDocuments))
		return
	}
	seenIndices := make(map[int]struct{}, len(req.Documents))
	for _, doc := range req.Documents {
		if doc.Index < 0 {
			s.writeError(w, http.StatusBadRequest, "schema document index must be non-negative")
			return
		}
		if _, exists := seenIndices[doc.Index]; exists {
			s.writeError(w, http.StatusBadRequest, fmt.Sprintf("duplicate schema document index %d", doc.Index))
			return
		}
		seenIndices[doc.Index] = struct{}{}
	}

	response := yamlSchemaResponse{
		Documents: make([]yamlSchemaDocument, 0, len(req.Documents)),
		Bundles:   make(map[string]yamlSchemaBundle),
	}
	groups := make(map[string]*requestedSchemaGroup)
	for _, doc := range req.Documents {
		path, err := openAPIPathForVersion(doc.APIVersion)
		if err != nil || strings.TrimSpace(doc.Kind) == "" {
			message := "apiVersion and kind are required"
			if err != nil {
				message = err.Error()
			}
			response.Documents = append(response.Documents, yamlSchemaDocument{Index: doc.Index, Status: "unavailable", Error: message})
			continue
		}
		group := groups[path]
		if group == nil {
			group = &requestedSchemaGroup{path: path}
			groups[path] = group
		}
		group.documents = append(group.documents, doc)
	}

	groupPaths := make([]string, 0, len(groups))
	for path := range groups {
		groupPaths = append(groupPaths, path)
	}
	sort.Strings(groupPaths)
	if len(groupPaths) == 0 {
		sort.Slice(response.Documents, func(i, j int) bool { return response.Documents[i].Index < response.Documents[j].Index })
		s.writeJSON(w, response)
		return
	}
	config, contextName := s.getConfigSnapshotForRequest(r)
	if config == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster OpenAPI schemas are unavailable: cluster config not available — check cluster connection")
		return
	}
	principal := contextName + "\x00" + config.Host + "\x00" + yamlSchemaCachePrincipal(r)
	paths, err := s.yamlSchemaPathsForRequest(config, principal)
	if err != nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster OpenAPI schemas are unavailable: "+err.Error())
		return
	}
	responseBytes := 0
	for _, path := range groupPaths {
		group := groups[path]
		gv, ok := paths[path]
		if !ok {
			for _, doc := range group.documents {
				response.Documents = append(response.Documents, yamlSchemaDocument{Index: doc.Index, Status: "unavailable", Error: "cluster does not publish an OpenAPI schema for " + doc.APIVersion})
			}
			continue
		}
		cacheKey := principal + "\x00" + path + "\x00" + gv.ServerRelativeURL()
		s.yamlSchemaMu.Lock()
		raw := s.yamlSchemaCache[cacheKey]
		s.yamlSchemaMu.Unlock()
		if raw == nil {
			raw, err = s.yamlSchemaSourceForRequest(gv, cacheKey)
			if err != nil {
				for _, doc := range group.documents {
					response.Documents = append(response.Documents, yamlSchemaDocument{Index: doc.Index, Status: "unavailable", Error: err.Error()})
				}
				continue
			}
		}

		bundleKey := cacheKey + "\x00" + yamlSchemaDocumentSignature(group.documents)
		s.yamlSchemaMu.Lock()
		cachedBundle, bundleCached := s.yamlSchemaBundleCache[bundleKey]
		s.yamlSchemaMu.Unlock()
		if !bundleCached {
			bundle, indexedRoots, buildErr := buildYAMLSchemaBundle(raw, group.documents)
			if buildErr != nil {
				for _, doc := range group.documents {
					response.Documents = append(response.Documents, yamlSchemaDocument{Index: doc.Index, Status: "unavailable", Error: buildErr.Error()})
				}
				continue
			}
			encoded, marshalErr := json.Marshal(bundle)
			if marshalErr != nil {
				log.Printf("[schemas] Failed to marshal OpenAPI schema bundle for %s: %v", sanitizeForLog(path), marshalErr)
				for _, doc := range group.documents {
					response.Documents = append(response.Documents, yamlSchemaDocument{Index: doc.Index, Status: "unavailable", Error: "failed to encode cluster schema"})
				}
				continue
			}
			cachedBundle = yamlSchemaBundleCacheEntry{
				bundle: bundle,
				roots:  rootsBySchemaIdentity(group.documents, indexedRoots),
				size:   len(encoded),
			}
			s.cacheYAMLSchemaBundle(bundleKey, cachedBundle)
		}
		if responseBytes+cachedBundle.size > maxYAMLSchemaResponseBytes {
			for _, doc := range group.documents {
				response.Documents = append(response.Documents, yamlSchemaDocument{Index: doc.Index, Status: "unavailable", Error: "schema exceeds Radar's safe response limit"})
			}
			continue
		}
		responseBytes += cachedBundle.size
		response.Bundles[path] = cachedBundle.bundle
		for _, doc := range group.documents {
			root := cachedBundle.roots[yamlSchemaIdentityKey(doc)]
			if root == "" {
				response.Documents = append(response.Documents, yamlSchemaDocument{Index: doc.Index, Status: "unavailable", Error: "cluster schema does not include " + doc.APIVersion + " " + doc.Kind})
				continue
			}
			response.Documents = append(response.Documents, yamlSchemaDocument{
				Index:     doc.Index,
				Status:    "available",
				BundleKey: path,
				SchemaRef: "#/definitions/" + root,
			})
		}
	}

	sort.Slice(response.Documents, func(i, j int) bool { return response.Documents[i].Index < response.Documents[j].Index })
	s.writeJSON(w, response)
}

func (s *Server) yamlSchemaSourceForRequest(gv openapi.GroupVersion, cacheKey string) ([]byte, error) {
	result, err, _ := s.yamlSchemaFetchGroup.Do("source\x00"+cacheKey, func() (any, error) {
		s.yamlSchemaMu.Lock()
		raw := s.yamlSchemaCache[cacheKey]
		s.yamlSchemaMu.Unlock()
		if raw != nil {
			return raw, nil
		}
		raw, err := gv.Schema("application/json")
		if err != nil {
			return nil, fmt.Errorf("failed to fetch cluster schema: %w", err)
		}
		if len(raw) > maxYAMLSchemaSourceBytes {
			return nil, fmt.Errorf("cluster OpenAPI schema exceeds Radar's safe source limit")
		}
		s.cacheYAMLSchemaSource(cacheKey, raw)
		return raw, nil
	})
	if err != nil {
		return nil, err
	}
	return result.([]byte), nil
}

func yamlSchemaCachePrincipal(r *http.Request) string {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		return "local"
	}
	groups := append([]string(nil), user.Groups...)
	sort.Strings(groups)
	return user.Username + "\x00" + strings.Join(groups, "\x00")
}

func (s *Server) yamlSchemaPathsForRequest(config *rest.Config, principal string) (map[string]openapi.GroupVersion, error) {
	now := time.Now()
	s.yamlSchemaMu.Lock()
	cached := s.yamlSchemaPathCache[principal]
	s.yamlSchemaMu.Unlock()
	if cached.paths != nil && now.Before(cached.expiresAt) {
		return cached.paths, nil
	}

	result, err, _ := s.yamlSchemaFetchGroup.Do("paths\x00"+principal, func() (any, error) {
		now := time.Now()
		s.yamlSchemaMu.Lock()
		cached := s.yamlSchemaPathCache[principal]
		s.yamlSchemaMu.Unlock()
		if cached.paths != nil && now.Before(cached.expiresAt) {
			return cached.paths, nil
		}

		requestConfig := rest.CopyConfig(config)
		requestConfig.Timeout = 10 * time.Second
		discoveryClient, err := discovery.NewDiscoveryClientForConfig(requestConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to create schema client: %w", err)
		}
		paths, err := discoveryClient.OpenAPIV3().Paths()
		if err != nil {
			return nil, err
		}
		s.cacheYAMLSchemaPaths(principal, paths, now)
		return paths, nil
	})
	if err != nil {
		return nil, err
	}
	return result.(map[string]openapi.GroupVersion), nil
}

func (s *Server) cacheYAMLSchemaPaths(principal string, paths map[string]openapi.GroupVersion, now time.Time) {
	s.yamlSchemaMu.Lock()
	defer s.yamlSchemaMu.Unlock()
	for key, entry := range s.yamlSchemaPathCache {
		if !now.Before(entry.expiresAt) {
			delete(s.yamlSchemaPathCache, key)
		}
	}
	if len(s.yamlSchemaPathCache) >= maxYAMLSchemaCacheEntries {
		clear(s.yamlSchemaPathCache)
	}
	s.yamlSchemaPathCache[principal] = yamlSchemaPathCacheEntry{paths: paths, expiresAt: now.Add(yamlSchemaPathCacheTTL)}
}

func (s *Server) cacheYAMLSchemaSource(key string, raw []byte) {
	s.yamlSchemaMu.Lock()
	defer s.yamlSchemaMu.Unlock()
	if s.yamlSchemaCache[key] != nil {
		return
	}
	if len(s.yamlSchemaCache)+len(s.yamlSchemaBundleCache) >= maxYAMLSchemaCacheEntries || s.yamlSchemaCacheBytes+len(raw) > maxYAMLSchemaCacheBytes {
		clear(s.yamlSchemaCache)
		clear(s.yamlSchemaBundleCache)
		s.yamlSchemaCacheBytes = 0
	}
	s.yamlSchemaCache[key] = raw
	s.yamlSchemaCacheBytes += len(raw)
}

func (s *Server) cacheYAMLSchemaBundle(key string, entry yamlSchemaBundleCacheEntry) {
	s.yamlSchemaMu.Lock()
	defer s.yamlSchemaMu.Unlock()
	if _, exists := s.yamlSchemaBundleCache[key]; exists {
		return
	}
	if len(s.yamlSchemaCache)+len(s.yamlSchemaBundleCache) >= maxYAMLSchemaCacheEntries || s.yamlSchemaCacheBytes+entry.size > maxYAMLSchemaCacheBytes {
		clear(s.yamlSchemaCache)
		clear(s.yamlSchemaBundleCache)
		s.yamlSchemaCacheBytes = 0
	}
	s.yamlSchemaBundleCache[key] = entry
	s.yamlSchemaCacheBytes += entry.size
}

func yamlSchemaIdentityKey(doc yamlSchemaIdentity) string {
	return doc.APIVersion + "\x00" + doc.Kind
}

func yamlSchemaDocumentSignature(documents []yamlSchemaIdentity) string {
	unique := make(map[string]struct{}, len(documents))
	for _, doc := range documents {
		unique[yamlSchemaIdentityKey(doc)] = struct{}{}
	}
	keys := make([]string, 0, len(unique))
	for key := range unique {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return strings.Join(keys, "\x01")
}

func rootsBySchemaIdentity(documents []yamlSchemaIdentity, indexedRoots map[int]string) map[string]string {
	roots := make(map[string]string, len(documents))
	for _, doc := range documents {
		roots[yamlSchemaIdentityKey(doc)] = indexedRoots[doc.Index]
	}
	return roots
}

func openAPIPathForVersion(apiVersion string) (string, error) {
	parts := strings.Split(strings.TrimSpace(apiVersion), "/")
	if len(parts) == 1 && parts[0] != "" {
		return "api/" + parts[0], nil
	}
	if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
		return "apis/" + parts[0] + "/" + parts[1], nil
	}
	return "", fmt.Errorf("invalid apiVersion %q", apiVersion)
}

func buildYAMLSchemaBundle(raw []byte, documents []yamlSchemaIdentity) (yamlSchemaBundle, map[int]string, error) {
	var openAPI map[string]any
	if err := json.Unmarshal(raw, &openAPI); err != nil {
		return yamlSchemaBundle{}, nil, fmt.Errorf("cluster returned invalid OpenAPI JSON: %w", err)
	}
	components, _ := openAPI["components"].(map[string]any)
	schemas, _ := components["schemas"].(map[string]any)
	if len(schemas) == 0 {
		return yamlSchemaBundle{}, nil, fmt.Errorf("cluster OpenAPI document has no component schemas")
	}

	roots := make(map[int]string, len(documents))
	selected := make(map[string]any)
	for _, doc := range documents {
		group, version, _ := strings.Cut(doc.APIVersion, "/")
		if version == "" {
			version = group
			group = ""
		}
		root := findGVKSchema(schemas, group, version, doc.Kind)
		if root == "" {
			continue
		}
		roots[doc.Index] = root
		collectSchemaDefinitions(root, schemas, selected)
	}
	for _, schemaValue := range selected {
		rewriteOpenAPIRefs(schemaValue)
		normalizeKubernetesJSONSchema(schemaValue)
	}
	return yamlSchemaBundle{Definitions: selected}, roots, nil
}

func findGVKSchema(schemas map[string]any, group, version, kind string) string {
	names := make([]string, 0, len(schemas))
	for name := range schemas {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		schemaValue, _ := schemas[name].(map[string]any)
		gvks := schemaValue["x-kubernetes-group-version-kind"]
		if gvkMatches(gvks, group, version, kind) {
			return name
		}
	}
	return ""
}

func gvkMatches(value any, group, version, kind string) bool {
	match := func(candidate any) bool {
		entry, _ := candidate.(map[string]any)
		return entry["group"] == group && entry["version"] == version && entry["kind"] == kind
	}
	if entries, ok := value.([]any); ok {
		for _, entry := range entries {
			if match(entry) {
				return true
			}
		}
		return false
	}
	return match(value)
}

func collectSchemaDefinitions(name string, all, selected map[string]any) {
	if _, exists := selected[name]; exists {
		return
	}
	value, exists := all[name]
	if !exists {
		return
	}
	selected[name] = value
	refs := make(map[string]struct{})
	collectOpenAPIRefs(value, refs)
	for ref := range refs {
		collectSchemaDefinitions(ref, all, selected)
	}
}

func collectOpenAPIRefs(value any, refs map[string]struct{}) {
	typed, ok := value.(map[string]any)
	if !ok {
		return
	}
	if ref, ok := typed["$ref"].(string); ok && strings.HasPrefix(ref, "#/components/schemas/") {
		refs[strings.TrimPrefix(ref, "#/components/schemas/")] = struct{}{}
	}
	visitOpenAPISubschemas(typed, func(child any) {
		collectOpenAPIRefs(child, refs)
	})
}

func visitOpenAPISubschemas(schemaValue map[string]any, visit func(any)) {
	for _, key := range []string{
		"items", "additionalItems", "additionalProperties", "contains", "propertyNames", "not", "if", "then", "else",
	} {
		if child, exists := schemaValue[key]; exists {
			if children, ok := child.([]any); ok {
				for _, item := range children {
					visit(item)
				}
			} else {
				visit(child)
			}
		}
	}
	for _, key := range []string{"allOf", "anyOf", "oneOf", "prefixItems"} {
		if children, ok := schemaValue[key].([]any); ok {
			for _, child := range children {
				visit(child)
			}
		}
	}
	for _, key := range []string{"properties", "patternProperties", "definitions", "$defs", "dependentSchemas", "dependencies"} {
		if children, ok := schemaValue[key].(map[string]any); ok {
			for _, child := range children {
				visit(child)
			}
		}
	}
}

func rewriteOpenAPIRefs(value any) {
	typed, ok := value.(map[string]any)
	if !ok {
		return
	}
	if ref, ok := typed["$ref"].(string); ok {
		typed["$ref"] = strings.Replace(ref, "#/components/schemas/", "#/definitions/", 1)
	}
	visitOpenAPISubschemas(typed, rewriteOpenAPIRefs)
}

func normalizeKubernetesJSONSchema(value any) {
	typed, ok := value.(map[string]any)
	if !ok {
		return
	}
	if typed["deprecated"] == true && typed["deprecationMessage"] == nil {
		typed["deprecationMessage"] = deprecatedSchemaAdvisory
	}
	if typed["format"] == "int-or-string" {
		delete(typed, "type")
		typed["oneOf"] = []any{map[string]any{"type": "integer"}, map[string]any{"type": "string"}}
	}
	if typed["nullable"] == true {
		if schemaType, ok := typed["type"].(string); ok {
			typed["type"] = []any{schemaType, "null"}
		}
	}
	if typed["x-kubernetes-preserve-unknown-fields"] == true && typed["additionalProperties"] == nil {
		typed["additionalProperties"] = true
	}
	visitOpenAPISubschemas(typed, normalizeKubernetesJSONSchema)
}
