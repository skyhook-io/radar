package prometheus

import (
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
)

func testClient(t *testing.T, name string, cache *k8s.ResourceCache) *Client {
	t.Helper()
	client := NewClient(nil, nil, name, nil, false, cache)
	t.Cleanup(func() { _ = client.Close() })
	return client
}
