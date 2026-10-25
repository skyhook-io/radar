package helm

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"helm.sh/helm/v3/pkg/action"

	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

func validSourceUpgradeRoute(w http.ResponseWriter, r *http.Request, req ApplyValuesRequest) bool {
	ctx := chi.RouteContext(r.Context())
	dedicated := ctx != nil && strings.Contains(ctx.RoutePattern(), "/{name}/source/")
	if dedicated != (req.SourceUpgrade != nil) {
		writeError(w, http.StatusBadRequest, "explicit source selections require the source preview or source upgrade endpoint and a sourceUpgrade request")
		return false
	}
	return true
}

func sourceErrorCode(err error) string {
	if errors.Is(err, errChartDigest) {
		return "chart_digest_failed"
	}
	if errors.Is(err, errSourceRequired) {
		return "chart_source_required"
	}
	if errors.Is(err, errSourceConflict) {
		return "chart_source_changed"
	}
	return ""
}

func decodeUpgradeRequest(body io.Reader) (ApplyValuesRequest, error) {
	var req ApplyValuesRequest
	if body == nil {
		return req, nil
	}
	err := json.NewDecoder(body).Decode(&req)
	if errors.Is(err, io.EOF) {
		err = nil
	}
	return req, err
}

func (c *Client) upgradeRequestAsUser(namespace, name, version, repository string, req ApplyValuesRequest, user *pkgauth.User, progress func(string, string, string)) error {
	cfg, err := c.getActionConfigForUser(namespace, user)
	if err != nil {
		return err
	}
	if req.SourceUpgrade != nil {
		_, err = c.sourceUpgradeWith(cfg, name, version, req.Values, *req.SourceUpgrade, false, progress)
		return err
	}
	if req.Values != nil {
		return c.upgradeWithValues(cfg, name, version, repository, req.Values, progress)
	}
	return c.upgradeWith(cfg, name, version, repository, progress)
}

func (h *Handlers) handleSourceVersions(w http.ResponseWriter, r *http.Request) {
	c := GetClient()
	if c == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}
	cfg, err := c.getActionConfigForUser(chi.URLParam(r, "namespace"), userCreds(r))
	if err != nil {
		writeReleaseReadError(w, err)
		return
	}
	rel, err := action.NewGet(cfg).Run(chi.URLParam(r, "name"))
	if err != nil {
		writeReleaseReadError(w, err)
		return
	}
	if rel.Chart == nil || rel.Chart.Metadata == nil {
		writeError(w, http.StatusBadRequest, "Release has no usable chart metadata")
		return
	}
	q := r.URL.Query()
	versions, err := c.sourceVersions(ChartSourceCandidate{Type: q.Get("type"), Reference: q.Get("reference"), URL: q.Get("url")}, rel.Chart.Metadata.Name)
	if err != nil {
		writeChartSourceOperationError(w, err, "read-source-versions", chi.URLParam(r, "namespace"), rel.Name)
		return
	}
	if versions == nil {
		versions = []string{}
	}
	writeJSON(w, versions)
}
