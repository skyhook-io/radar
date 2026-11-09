package k8score

import (
	"context"
	"errors"
	"fmt"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var ErrResourceListTooLarge = errors.New("resource list exceeds the item budget")

// ListDirectBounded follows even short pages until the scope is complete or
// the item budget is exceeded; a continuation token alone does not prove size.
func (d *DynamicResourceCache) ListDirectBounded(ctx context.Context, gvr schema.GroupVersionResource, namespace string, maxItems int) ([]*unstructured.Unstructured, error) {
	if maxItems < 1 {
		return nil, fmt.Errorf("maxItems must be positive")
	}
	options := metav1.ListOptions{}
	var items []*unstructured.Unstructured
	for {
		options.Limit = int64(maxItems - len(items) + 1)
		page, err := d.config.DynamicClient.Resource(gvr).Namespace(namespace).List(ctx, options)
		if err != nil {
			return nil, err
		}
		if len(items)+len(page.Items) > maxItems {
			return nil, ErrResourceListTooLarge
		}
		for i := range page.Items {
			items = append(items, StripUnstructuredFields(&page.Items[i]))
		}
		if page.GetContinue() == "" {
			return items, nil
		}
		options.Continue = page.GetContinue()
	}
}
