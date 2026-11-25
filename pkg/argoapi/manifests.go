package argoapi

import (
	"context"
	"errors"
	"net/url"
)

// ApplicationGeneration identifies the Application whose current source configuration
// GetManifests will render. The caller checks it before and after both renders.
func (c *Client) ApplicationGeneration(ctx context.Context, q ApplicationQuery) (string, int64, error) {
	if q.AppName == "" {
		return "", 0, errors.New("argoapi: ApplicationGeneration requires AppName")
	}
	var out struct {
		Metadata struct {
			UID        string
			Generation int64
		}
	}
	params := url.Values{"fields": {"metadata.uid,metadata.generation"}, "appNamespace": {q.AppNamespace}}
	setNonEmpty(params, "project", q.Project)
	err := c.get(ctx, "/api/v1/applications/"+url.PathEscape(q.AppName), params, &out)
	return out.Metadata.UID, out.Metadata.Generation, err
}

// RevisionManifests reads the single-source Application's desired resources at a pinned commit.
func (c *Client) RevisionManifests(ctx context.Context, q ApplicationQuery, revision string) ([]string, error) {
	if q.AppName == "" || revision == "" {
		return nil, errors.New("argoapi: RevisionManifests requires AppName and revision")
	}
	var out *struct {
		Manifests []string
		Revision  string
	}
	params := url.Values{"revision": {revision}, "appNamespace": {q.AppNamespace}}
	setNonEmpty(params, "project", q.Project)
	if err := c.get(ctx, "/api/v1/applications/"+url.PathEscape(q.AppName)+"/manifests", params, &out); err != nil {
		return nil, err
	}
	// Current Argo concatenates repo-server results and omits the resolved
	// revision, even for single-source apps. Older versions forward it.
	// An empty successful render can omit the repeated manifests field.
	if out == nil || (out.Revision != "" && out.Revision != revision) {
		return nil, errors.New("Argo did not return the requested manifest revision")
	}
	return out.Manifests, nil
}
