package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/cloudinstall"
	"github.com/skyhook-io/radar/internal/config"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

var enrollmentSecretKeys = map[string]bool{
	cloud.SecretKeyToken: true, cloud.SecretKeyClusterID: true, cloud.SecretKeyHubOrigin: true,
	cloud.SecretKeyWSSURL: true, cloud.SecretKeyNonce: true, cloud.SecretKeyNonceOrigin: true,
	cloud.SecretKeyStatus: true, cloud.SecretKeyError: true, cloud.SecretKeyAt: true,
}

func cloudResetEnrollment(args []string, out, errOut io.Writer) int {
	fs := flag.NewFlagSet("cloud reset-enrollment", flag.ContinueOnError)
	var flagOutput bytes.Buffer
	fs.SetOutput(&flagOutput)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: radar cloud reset-enrollment [--context NAME] [--namespace NS] [--release NAME] [--secret NAME]")
		fmt.Fprintln(fs.Output(), "Clears the token Secret of a Radar that joined with an enrollment key, so it joins the Hub again.")
		fmt.Fprintln(fs.Output(), "Radar's Deployment in the namespace names the Secret; pass --release when the namespace has more than one.")
		fs.PrintDefaults()
	}
	namespace := fs.String("namespace", cloudinstall.DefaultInstallNamespace, "Namespace Radar runs in")
	release := fs.String("release", "", "Helm release of the Radar to reset, when the namespace has more than one")
	secretName := fs.String("secret", "", "Token Secret to clear, instead of the one Radar's Deployment names")
	contextName := fs.String("context", "", "Kubernetes context to use (default: current context)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, _ = io.Copy(out, &flagOutput)
			return 0
		}
		_, _ = io.Copy(errOut, &flagOutput)
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(errOut, "cloud reset-enrollment: unexpected argument %q\n", fs.Arg(0))
		return 2
	}

	kubeconfigSelection, err := selectCloudCommandKubeconfig(config.Load())
	if err != nil {
		fmt.Fprintf(errOut, "cloud reset-enrollment: %v\n", err)
		return 1
	}
	ctxName, err := resolveCloudInstallContext(kubeconfigSelection.path, strings.TrimSpace(*contextName))
	if err != nil {
		fmt.Fprintf(errOut, "cloud reset-enrollment: %v\n", err)
		return 1
	}
	clients, err := buildLocalKubernetesClients(kubeconfigSelection.path, ctxName)
	if err != nil {
		fmt.Fprintf(errOut, "cloud reset-enrollment: %v\n", err)
		return 1
	}
	ctx, cancel := signalContext()
	defer cancel()
	fmt.Fprintf(out, "Kubernetes context: %q\n", ctxName)
	name := strings.TrimSpace(*secretName)
	if name == "" {
		found, err := findEnrollmentTokenSecret(ctx, clients.Kubernetes, *namespace, strings.TrimSpace(*release))
		if err != nil {
			fmt.Fprintf(errOut, "cloud reset-enrollment: %v\n", err)
			return 1
		}
		name = found
	}
	return runResetEnrollment(ctx, clients.Kubernetes, *namespace, name, out, errOut)
}

// findEnrollmentTokenSecret reads which token Secret the enrolled Radar in
// namespace uses, from its Deployment, the way `radar cloud status` does. The
// name depends on the release and on cloud.tokenSecretName, so a fixed
// default would clear the wrong Secret or none.
func findEnrollmentTokenSecret(ctx context.Context, kc kubernetes.Interface, namespace, release string) (string, error) {
	result, err := cloudinstall.DiscoverRadarTargets(ctx, kc, nil, cloudinstall.DiscoveryOptions{Namespace: namespace, ReleaseName: release})
	if err != nil {
		return "", fmt.Errorf("could not find Radar in namespace %q to learn its token Secret: %w. Pass the Secret with --secret", namespace, err)
	}
	candidates := result.Namespace
	if release != "" {
		candidates = result.Selected
	}
	var enrolled []cloudinstall.RadarTarget
	for _, target := range candidates {
		if target.Runtime.UsesEnrollment() {
			enrolled = append(enrolled, target)
		}
	}
	switch {
	case len(enrolled) == 0 && release != "":
		return "", fmt.Errorf("no Radar with release %q in namespace %q joins with an enrollment key, so nothing was reset", release, namespace)
	case len(enrolled) == 0:
		return "", fmt.Errorf("no Radar in namespace %q joins with an enrollment key, so nothing was reset. Check --namespace, or pass the Secret with --secret", namespace)
	case len(enrolled) > 1:
		return "", fmt.Errorf("namespace %q has more than one Radar that joins with an enrollment key; choose one with --release:\n%s", namespace, strings.TrimRight(cloudinstall.FormatTargets(enrolled), "\n"))
	}
	target := enrolled[0]
	if target.Runtime.EnrollmentTokenSecret == "" {
		return "", fmt.Errorf("Deployment %s/%s does not name its token Secret, so nothing was reset. Pass it with --secret", target.Namespace, target.DeploymentName)
	}
	return target.Runtime.EnrollmentTokenSecret, nil
}

func runResetEnrollment(ctx context.Context, kc kubernetes.Interface, namespace, name string, out, errOut io.Writer) int {
	ref := namespace + "/" + name
	cleared, err := resetEnrollmentSecret(ctx, kc, namespace, name)
	switch {
	case apierrors.IsNotFound(err):
		fmt.Fprintf(errOut, "Secret %s was not found, so nothing was reset.\n", ref)
		fmt.Fprintln(errOut, "Check that --namespace is where Radar runs. Without --secret, the Secret named by Radar's Deployment is used.")
		return 1
	case apierrors.IsForbidden(err):
		fmt.Fprintf(errOut, "Kubernetes refused access to Secret %s, so nothing was reset.\n", ref)
		fmt.Fprintln(errOut, "Run this with an account that can get and update that Secret.")
		fmt.Fprintf(errOut, "details: %v\n", err)
		return 1
	case err != nil:
		fmt.Fprintf(errOut, "Secret %s was not reset: %v\n", ref, err)
		return 1
	case !cleared:
		fmt.Fprintf(out, "Secret %s is already empty. An empty Secret already makes Radar join with its enrollment key.\n", ref)
		return 0
	default:
		fmt.Fprintf(out, "Cleared Secret %s. Radar watches it and starts a new join with its enrollment key, without a restart.\n", ref)
		fmt.Fprintln(out, "Check progress with: radar cloud status")
		return 0
	}
}

// resetEnrollmentSecret clears every data key of the token Secret with a
// conditional update. It refuses a Secret holding keys Radar never writes, so
// a wrong --secret cannot wipe an unrelated Secret.
func resetEnrollmentSecret(ctx context.Context, kc kubernetes.Interface, namespace, name string) (bool, error) {
	for attempt := 0; ; attempt++ {
		secret, err := kc.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		var foreign []string
		for k := range secret.Data {
			if !enrollmentSecretKeys[k] {
				foreign = append(foreign, k)
			}
		}
		if len(foreign) > 0 {
			sort.Strings(foreign)
			return false, fmt.Errorf("it holds keys Radar does not write (%s), so it is not a Radar token Secret", strings.Join(foreign, ", "))
		}
		if len(secret.Data) == 0 {
			return false, nil
		}
		secret.Data = nil
		secret.StringData = nil
		_, err = kc.CoreV1().Secrets(namespace).Update(ctx, secret, metav1.UpdateOptions{})
		if apierrors.IsConflict(err) && attempt < 3 {
			continue
		}
		return err == nil, err
	}
}
