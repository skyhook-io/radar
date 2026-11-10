package main

import (
	"errors"
	"fmt"

	"github.com/skyhook-io/radar/internal/app"
	"github.com/skyhook-io/radar/internal/k8s"
)

// withNamespaceCountCheck adds the CLI's list-length check to the result of
// app.InitializeK8s. The CLI runs it before reading the kubeconfig, so a
// kubeconfig failure must not hide it, or a later context switch would
// connect around it. InitializeK8s's own count error wins: it also counts a
// distinct context namespace.
func withNamespaceCountCheck(initErr error, namespaces []string) error {
	var fanout *app.NamespaceFanoutError
	if errors.As(initErr, &fanout) {
		return initErr
	}
	if err := app.CheckNamespaceCount(namespaces, k8s.MaxScopeCandidates); err != nil {
		return err
	}
	return initErr
}

// initFailureStatus is the connection status the window shows when
// Kubernetes initialization fails. A namespace list longer than the probe
// cap is a settings problem, not a kubeconfig one: the CLI refuses to start
// on it, so Desktop reports it with the setting to change and blocks
// all cluster access (blockConnect) until Radar restarts with new settings.
func initFailureStatus(err error, contextName string, namespacesFromFlag bool, configPath string) (status k8s.ConnectionStatus, blockConnect bool) {
	status = k8s.ConnectionStatus{
		State:     k8s.StateDisconnected,
		Context:   contextName,
		Error:     err.Error(),
		ErrorType: "config",
	}
	var fanout *app.NamespaceFanoutError
	if !errors.As(err, &fanout) {
		return status, false
	}
	status.ErrorType = "settings"
	status.Error = namespaceSettingsMessage(fanout, namespacesFromFlag, configPath)
	return status, true
}

func namespaceSettingsMessage(e *app.NamespaceFanoutError, namespacesFromFlag bool, configPath string) string {
	listed := fmt.Sprintf(`%s lists %d namespaces in "namespaces"`, configPath, e.Listed)
	file := "that file"
	if namespacesFromFlag {
		listed = fmt.Sprintf("--namespaces lists %d namespaces", e.Listed)
		file = configPath
	}
	if e.ContextNamespace == "" {
		return fmt.Sprintf(`%s, but Radar checks its access in at most %d namespaces. Set "maxScopeCandidates" to %d in %s to check all of them, then quit and reopen Radar.`,
			listed, e.Cap, e.Candidates(), file)
	}
	fix := fmt.Sprintf(`Set "maxScopeCandidates" to %d in %s`, e.Candidates(), file)
	if e.Listed <= e.Cap {
		// Swapping a listed namespace for the context one fits only when the
		// list alone is within the cap.
		fix += fmt.Sprintf(`, or replace one listed namespace with %q`, e.ContextNamespace)
	}
	return fmt.Sprintf(`%s, and the kubeconfig context adds namespace %q, so Radar would check %d. It checks its access in at most %d. %s, then quit and reopen Radar.`,
		listed, e.ContextNamespace, e.Candidates(), e.Cap, fix)
}
