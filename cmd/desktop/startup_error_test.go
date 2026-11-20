package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/app"
	"github.com/skyhook-io/radar/internal/k8s"
)

func TestInitFailureStatus(t *testing.T) {
	const path = "/home/u/.radar/config.json"

	t.Run("other init failures stay kubeconfig problems and don't block", func(t *testing.T) {
		status, block := initFailureStatus(errors.New("no kubeconfig"), "prod", false, path)
		if status.ErrorType != "config" || status.State != k8s.StateDisconnected || status.Context != "prod" || block {
			t.Fatalf("status = %+v, block = %v", status, block)
		}
	})

	t.Run("too many namespaces from config.json names the value that fits", func(t *testing.T) {
		err := fmt.Errorf("wrapped: %w", &app.NamespaceFanoutError{Listed: 25, Cap: 20})
		status, block := initFailureStatus(err, "prod", false, path)
		if status.ErrorType != "settings" || !block {
			t.Fatalf("status = %+v, block = %v", status, block)
		}
		if !strings.Contains(status.Error, path) || !strings.Contains(status.Error, "25") {
			t.Fatalf("message must name the file and the value to set: %q", status.Error)
		}
	})

	t.Run("a distinct context namespace counts toward the value", func(t *testing.T) {
		status, _ := initFailureStatus(&app.NamespaceFanoutError{Listed: 20, ContextNamespace: "ctx-home", Cap: 20}, "prod", false, path)
		if !strings.Contains(status.Error, "21") || !strings.Contains(status.Error, "ctx-home") {
			t.Fatalf("message must suggest 21 and name the context namespace: %q", status.Error)
		}
	})

	t.Run("swapping in the context namespace is offered only when it would fit", func(t *testing.T) {
		long, _ := initFailureStatus(&app.NamespaceFanoutError{Listed: 25, ContextNamespace: "ctx-home", Cap: 20}, "prod", false, path)
		if !strings.Contains(long.Error, "26") || strings.Count(long.Error, "ctx-home") != 1 {
			t.Fatalf("a 25-name list can't fit by swapping; only the cap fix applies: %q", long.Error)
		}
	})

	t.Run("namespaces passed as flags still point at the Desktop setting", func(t *testing.T) {
		status, block := initFailureStatus(&app.NamespaceFanoutError{Listed: 25, Cap: 20}, "prod", true, path)
		if status.ErrorType != "settings" || !block || !strings.Contains(status.Error, "--namespaces") || !strings.Contains(status.Error, path) {
			t.Fatalf("status = %+v, block = %v", status, block)
		}
	})
}

func TestWithNamespaceCountCheck(t *testing.T) {
	names := make([]string, k8s.MaxScopeCandidates+5)
	for i := range names {
		names[i] = fmt.Sprintf("ns-%d", i)
	}
	var fanout *app.NamespaceFanoutError

	kubeconfigErr := errors.New("no kubeconfig")
	if err := withNamespaceCountCheck(kubeconfigErr, names); !errors.As(err, &fanout) || fanout.Listed != len(names) {
		t.Fatalf("a kubeconfig failure must not hide a list over the cap: %v", err)
	}
	if err := withNamespaceCountCheck(kubeconfigErr, names[:3]); !errors.Is(err, kubeconfigErr) {
		t.Fatalf("a list within the cap keeps the kubeconfig error: %v", err)
	}
	if err := withNamespaceCountCheck(nil, names[:3]); err != nil {
		t.Fatalf("nothing to report: %v", err)
	}
	withContext := &app.NamespaceFanoutError{Listed: len(names), ContextNamespace: "ctx", Cap: k8s.MaxScopeCandidates}
	if err := withNamespaceCountCheck(withContext, names); !errors.As(err, &fanout) || fanout.ContextNamespace != "ctx" {
		t.Fatalf("the init check, which counts the context namespace, must win: %v", err)
	}
}
