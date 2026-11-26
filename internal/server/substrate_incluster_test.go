package server

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	authv1 "k8s.io/api/authentication/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	ktesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/k8s"
	substrateapi "github.com/skyhook-io/radar/internal/substrate"
)

// inClusterRadar connects Radar as a Helm install does: the Pod's credentials,
// no kubeconfig context and so no profile binding.
func inClusterRadar(t *testing.T) {
	t.Helper()
	old := k8s.ForceInCluster
	k8s.ForceInCluster = true
	t.Cleanup(func() { k8s.ForceInCluster = old })
	t.Cleanup(k8s.SetTestConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected}))
	cfg := &rest.Config{Host: "https://10.0.0.1:443"}
	prevCfg := k8s.SetTestConfig(cfg)
	t.Cleanup(func() { k8s.SetTestConfig(prevCfg) })
	prevClient := k8s.SetTestClient(kubernetes.NewForConfigOrDie(&rest.Config{Host: "http://127.0.0.1:1"}))
	t.Cleanup(func() { k8s.SetTestClient(prevClient) })
	if target, err := k8s.CurrentProfileTarget(); err == nil || target.Binding != "" {
		t.Fatalf("fixture has a profile binding %q (err %v); an in-cluster Radar has none", target.Binding, err)
	}
}

func mintingCaller(fail error) (substrateapi.Caller, *int) {
	c := fake.NewClientset()
	mints := 0
	c.PrependReactor("create", "serviceaccounts", func(action ktesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "token" {
			return false, nil, nil
		}
		mints++
		if fail != nil {
			return true, nil, fail
		}
		req := action.(ktesting.CreateAction).GetObject().(*authv1.TokenRequest).DeepCopy()
		req.Status.Token = "caller-token"
		req.Status.ExpirationTimestamp = metav1.NewTime(time.Now().Add(time.Hour))
		return true, req, nil
	})
	return substrateapi.Caller{Client: c}, &mints
}

// An in-cluster Radar reads Substrate for a signed-in caller through the
// production capture: the caller's own client mints, and a caller it refuses
// is told so, rather than both failing before the mint for want of a
// kubeconfig context.
func TestInClusterCallerReachesTheirOwnMint(t *testing.T) {
	inClusterRadar(t)
	k, err := currentSubstrateKube()
	if err != nil || k.Client == nil || k.Fingerprint == "" || k.Binding != "" {
		t.Fatalf("in-cluster capture = %+v, %v", k, err)
	}

	m := substrateapi.NewManager(substrateapi.NewStore(filepath.Join(t.TempDir(), "substrate.json")), currentSubstrateKube)
	t.Cleanup(m.Reset)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	bob, bobMints := mintingCaller(apierrors.NewForbidden(schema.GroupResource{Resource: "serviceaccounts/token"}, substrateapi.ClientServiceAccount, nil))
	bob.Identity = "bob"
	if _, tier := m.Snapshot(substrateapi.WithCaller(ctx, bob)); tier.Code != substrateapi.CodeMintDenied || *bobMints != 1 {
		t.Errorf("refused caller: tier %+v after %d mints, want mintDenied after one", tier, *bobMints)
	}

	alice, aliceMints := mintingCaller(nil)
	alice.Identity = "alice"
	_, tier := m.Snapshot(substrateapi.WithCaller(ctx, alice))
	if *aliceMints != 1 || tier.Code == substrateapi.CodeMintDenied || strings.Contains(tier.Reason, "kubeconfig") || strings.Contains(tier.Reason, "Not connected to Kubernetes") {
		t.Errorf("allowed caller: tier %+v after %d mints, want one mint and a read past it", tier, *aliceMints)
	}
}

// A local Radar still needs a kubeconfig context: its settings are saved per
// context, and none can be chosen without one.
func TestLocalCaptureNeedsAContext(t *testing.T) {
	inClusterRadar(t)
	k8s.ForceInCluster = false
	t.Cleanup(k8s.SetTestLocalMode())
	if _, err := currentSubstrateKube(); err == nil {
		t.Error("a local Radar without a kubeconfig context captured a cluster")
	}
}
