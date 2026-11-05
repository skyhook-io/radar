package helm

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

func TestCheckWritePermissionOperatorDisabled(t *testing.T) {
	previous := k8s.ForceDisableHelmWrite
	k8s.ForceDisableHelmWrite = true
	t.Cleanup(func() { k8s.ForceDisableHelmWrite = previous })
	for _, user := range []*pkgauth.User{nil, {Username: "alice"}} {
		for _, namespace := range []string{"", "team-a"} {
			ctx := pkgauth.ContextWithUser(context.Background(), user)
			status, err := CheckWritePermission(ctx, namespace)
			if user != nil && namespace == "" {
				if status != 0 || err != nil {
					t.Fatalf("authenticated cache refresh refused: %d %v", status, err)
				}
				continue
			}
			if status != http.StatusForbidden || err == nil || !strings.Contains(err.Error(), "--disable-helm-write") {
				t.Fatalf("user=%v namespace=%q: want operator-disabled refusal, got %d %v", user, namespace, status, err)
			}
		}
	}
}
