package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/skyhook-io/radar/internal/clusterreplay"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	name := flag.String("context", "", "kubeconfig context to capture (required)")
	out := flag.String("out", "", "destination .ndjson.gz (required; must not exist)")
	flag.Parse()
	if *name == "" || *out == "" {
		return fmt.Errorf("-context and -out are required")
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{CurrentContext: *name}).ClientConfig()
	if err != nil {
		return fmt.Errorf("load kubeconfig failed")
	}
	cfg.Timeout = 30 * time.Second
	d, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return fmt.Errorf("create discovery client failed")
	}
	c, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("create dynamic client failed")
	}
	// A failed list must not leave a plausible-looking complete snapshot.
	f, err := os.CreateTemp(filepath.Dir(*out), ".capture-*.partial")
	if err != nil {
		return err
	}
	partial := f.Name()
	defer os.Remove(partial)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	count, skipped, captureErr := clusterreplay.Capture(ctx, d, c, *name, f)
	closeErr := f.Close()
	if captureErr != nil {
		return fmt.Errorf("capture failed; no snapshot published (discovery failure or interrupted)")
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Link(partial, *out); err != nil {
		return err
	}
	fmt.Printf("Captured %d objects to %s\n", count, *out)
	if len(skipped) > 0 {
		fmt.Printf("Skipped %d resources whose lists failed; details are in the capture header\n", len(skipped))
	}
	return nil
}
