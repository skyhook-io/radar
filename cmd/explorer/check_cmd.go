package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/pager"

	"github.com/skyhook-io/radar/internal/images"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/resourceapi"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

const (
	java4ShellPodListBudget  = 30 * time.Second
	java4ShellMetadataBudget = 5 * time.Second
	java4ShellPodLimit       = 50000
)

func runCheckCommand(args []string, out, errOut io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(out, "Usage: radar check java4shell [flags]")
		return 0
	}
	if args[0] != "java4shell" {
		fmt.Fprintf(errOut, "radar check: unknown check %q\n", args[0])
		return 1
	}

	fs := flag.NewFlagSet("check java4shell", flag.ContinueOnError)
	var flagOutput bytes.Buffer
	fs.SetOutput(&flagOutput)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: radar check java4shell [-n NAMESPACE | -A] [-o table|json] [--fail-on-match]")
		fmt.Fprintln(fs.Output(), "Check declared Java versions in running images using the current Kubernetes context.")
		fs.PrintDefaults()
	}
	var namespace, output string
	var allNamespaces bool
	fs.StringVar(&namespace, "namespace", "", "Namespace to check (defaults to the context namespace)")
	fs.StringVar(&namespace, "n", "", "Shorthand for --namespace")
	fs.BoolVar(&allNamespaces, "all-namespaces", false, "Check Pods across all namespaces")
	fs.BoolVar(&allNamespaces, "A", false, "Shorthand for --all-namespaces")
	fs.StringVar(&output, "output", "table", "Output format (table or json)")
	fs.StringVar(&output, "o", "table", "Shorthand for --output")
	contextName := fs.String("context", "", "Kubernetes context (defaults to the current context)")
	kubeconfig := fs.String("kubeconfig", "", "Kubeconfig path (defaults to KUBECONFIG or the standard Kubernetes config)")
	failOnMatch := fs.Bool("fail-on-match", false, "Exit 2 when a container declares an affected Java version")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, _ = io.Copy(out, &flagOutput)
			return 0
		}
		_, _ = io.Copy(errOut, &flagOutput)
		return 1
	}
	if fs.NArg() != 0 || (namespace != "" && allNamespaces) || (output != "table" && output != "json") {
		fs.SetOutput(errOut)
		fs.Usage()
		return 1
	}

	fmt.Fprintln(errOut, "Loading Kubernetes configuration…")
	result, err := runJava4ShellCheck(*kubeconfig, *contextName, namespace, allNamespaces, errOut)
	if err == nil {
		err = writeJava4ShellCheck(out, output, result)
	}
	if err != nil {
		fmt.Fprintf(errOut, "radar check java4shell: %v\n", err)
		return 1
	}
	if *failOnMatch && result.Matches > 0 {
		return 2
	}
	return 0
}

func runJava4ShellCheck(kubeconfig, contextName, namespace string, allNamespaces bool, progress io.Writer) (images.Java4ShellReport, error) {
	if kubeconfig != "" {
		path, err := k8s.NormalizeKubeconfigPath(kubeconfig)
		if err != nil {
			return images.Java4ShellReport{}, err
		}
		kubeconfig = path
	}
	loader := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(connectLoadingRules(kubeconfig),
		&clientcmd.ConfigOverrides{CurrentContext: contextName})
	cfg, err := loader.ClientConfig()
	if err != nil {
		return images.Java4ShellReport{}, fmt.Errorf("load Kubernetes configuration: %w", err)
	}
	if namespace == "" && !allNamespaces {
		namespace, _, err = loader.Namespace()
		if err != nil {
			return images.Java4ShellReport{}, fmt.Errorf("read context namespace: %w", err)
		}
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return images.Java4ShellReport{}, fmt.Errorf("create Kubernetes client: %w", err)
	}

	ctx, stop := signalContext()
	defer stop()
	return checkJava4Shell(ctx, client, namespace, progress)
}

func checkJava4Shell(ctx context.Context, client kubernetes.Interface, namespace string, progress io.Writer) (images.Java4ShellReport, error) {
	scope := resourceapi.Scope{Type: resourceapi.ScopeAllNamespaces}
	if namespace != "" {
		scope = resourceapi.Scope{Type: resourceapi.ScopeNamespaces, Namespaces: resourceapi.Names([]string{namespace}, resourceapi.RESTSetLimit)}
		fmt.Fprintf(progress, "Listing Pods in namespace %q…\n", namespace)
	} else {
		fmt.Fprintln(progress, "Listing Pods across all namespaces…")
	}
	report := images.Java4ShellReport{Coverage: resourceapi.Coverage{
		Status: resourceapi.CoverageComplete, Scope: scope, Read: &scope, Gaps: []resourceapi.Gap{},
	}}

	pods := []images.Java4ShellPod{}
	nodes := map[string]*corev1.Node{}
	podPages := pager.New(func(ctx context.Context, options metav1.ListOptions) (runtime.Object, error) {
		return client.CoreV1().Pods(namespace).List(ctx, options)
	})
	listCtx, cancelList := context.WithTimeout(ctx, java4ShellPodListBudget)
	listErr := podPages.EachListItemWithAlloc(listCtx, metav1.ListOptions{
		FieldSelector: "status.phase!=Succeeded,status.phase!=Failed",
	}, func(obj runtime.Object) error {
		pod := obj.(*corev1.Pod)
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			return nil
		}
		if len(pods) == java4ShellPodLimit {
			return fmt.Errorf("more than %d Pods to check; select a smaller namespace scope", java4ShellPodLimit)
		}
		pods = append(pods, images.Java4ShellPod{Pod: pod})
		if pod.Spec.NodeName != "" {
			nodes[pod.Spec.NodeName] = nil
		}
		return nil
	})
	cancelList()
	if listErr != nil {
		return images.Java4ShellReport{}, fmt.Errorf("list Pods: %w", listErr)
	}

	if len(nodes) > 0 {
		fmt.Fprintln(progress, "Reading Node information…")
		nodeCtx, cancel := context.WithTimeout(ctx, java4ShellMetadataBudget)
		nodePages := pager.New(func(ctx context.Context, options metav1.ListOptions) (runtime.Object, error) {
			return client.CoreV1().Nodes().List(ctx, options)
		})
		// CPU placement stays unknown for any Node the caller cannot list.
		_ = nodePages.EachListItemWithAlloc(nodeCtx, metav1.ListOptions{}, func(obj runtime.Object) error {
			node := obj.(*corev1.Node)
			if _, needed := nodes[node.Name]; needed {
				nodes[node.Name] = node
			}
			return nil
		})
		cancel()
		for i := range pods {
			pods[i].Node = nodes[pods[i].Pod.Spec.NodeName]
		}
	}

	if len(pods) > 0 {
		fmt.Fprintln(progress, "Checking registry image metadata…")
	}
	accessCtx, cancelAccess := context.WithTimeout(ctx, java4ShellMetadataBudget)
	defer cancelAccess()
	secrets := map[types.NamespacedName]*corev1.Secret{}
	sets := map[types.NamespacedName]*appsv1.ReplicaSet{}
	var ownerDeadline time.Time
	var lastProgress time.Time
	var err error
	report.Java4ShellResult, err = images.DefaultRuntimeInventory(false).ScanJava4Shell(ctx, pods, func(ctx context.Context, pod *corev1.Pod) resourceid.Ref {
		if ownerDeadline.IsZero() {
			ownerDeadline = time.Now().Add(java4ShellMetadataBudget)
		}
		ownerCtx, cancel := context.WithDeadline(ctx, ownerDeadline)
		defer cancel()
		owner, ok, _ := k8s.PodWorkloadFromOwners(pod, func(namespace string, ref *metav1.OwnerReference) *appsv1.ReplicaSet {
			if resourceid.GroupFromAPIVersion(ref.APIVersion) != "apps" || ref.UID == "" {
				return nil
			}
			key := types.NamespacedName{Namespace: namespace, Name: ref.Name}
			rs, found := sets[key]
			if !found && ownerCtx.Err() == nil {
				var err error
				rs, err = client.AppsV1().ReplicaSets(namespace).Get(ownerCtx, ref.Name, metav1.GetOptions{})
				if err != nil {
					rs = nil
				}
				sets[key] = rs
			}
			if rs == nil || rs.UID != ref.UID {
				return nil
			}
			return rs
		})
		if !ok {
			return resourceid.NewRef("", "Pod", pod.Namespace, pod.Name)
		}
		return resourceid.NewRef(owner.Group, owner.Kind, pod.Namespace, owner.Name)
	}, func(pod *corev1.Pod) images.Java4ShellRegistryAccess {
		access := images.Java4ShellRegistryAccess{RadarCredentials: true}
		for _, ref := range pod.Spec.ImagePullSecrets {
			if ref.Name == "" || accessCtx.Err() != nil {
				continue
			}
			key := types.NamespacedName{Namespace: pod.Namespace, Name: ref.Name}
			secret, found := secrets[key]
			if !found {
				var err error
				secret, err = client.CoreV1().Secrets(pod.Namespace).Get(accessCtx, ref.Name, metav1.GetOptions{})
				if err != nil {
					secret = nil
				}
				secrets[key] = secret
			}
			if secret != nil && secret.Type == corev1.SecretTypeDockerConfigJson {
				access.PullSecrets = append(access.PullSecrets, secret)
			}
		}
		return access
	}, func(done, total int) {
		if total > 0 && (done == total || time.Since(lastProgress) >= time.Second) {
			fmt.Fprintf(progress, "Processed %d of %d image groups…\n", done, total)
			lastProgress = time.Now()
		}
	})
	if err != nil {
		return images.Java4ShellReport{}, err
	}
	if ctx.Err() != nil {
		return images.Java4ShellReport{}, ctx.Err()
	}
	return report, nil
}

func writeJava4ShellCheck(out io.Writer, output string, report images.Java4ShellReport) error {
	if output == "json" {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			CVE       string    `json:"cve"`
			CheckedAt time.Time `json:"checkedAt"`
			images.Java4ShellReport
		}{CVE: images.Java4ShellCVE, CheckedAt: time.Now().UTC(), Java4ShellReport: report})
	}

	var table bytes.Buffer
	w := tabwriter.NewWriter(&table, 0, 0, 2, ' ', 0)
	scope := "all namespaces"
	if report.Coverage.Scope.Namespaces != nil {
		scope = "namespace " + report.Coverage.Scope.Namespaces.Names[0]
	}
	fmt.Fprintf(w, "Java4Shell (%s) · %s\n", images.Java4ShellCVE, scope)
	unassessed := 0
	for _, count := range report.Unassessed {
		unassessed += count
	}
	noDeclaration := report.Unassessed["no_java_declaration"]
	switch {
	case report.Matches > 0:
		fmt.Fprintf(w, "%d running containers · %d with a declared Java version in an affected range\n", report.Containers, report.Matches)
	case report.Containers == 0:
		fmt.Fprintln(w, "No running containers in the requested scope")
	case noDeclaration == report.Containers:
		fmt.Fprintf(w, "Images for all %d running containers declared no Java version\n", report.Containers)
	case unassessed == report.Containers:
		fmt.Fprintf(w, "Java version could not be assessed in any of the %d running containers\n", report.Containers)
	default:
		fmt.Fprintf(w, "No matches among the %d containers whose declared Java version could be assessed\n", report.Containers-unassessed)
	}
	for _, major := range slices.Sorted(maps.Keys(report.UnaffectedByMajor)) {
		fmt.Fprintf(w, "Declared Java %d outside affected versions · %d running containers\n", major, report.UnaffectedByMajor[major])
	}
	if len(report.Findings) > 0 {
		fmt.Fprintln(w, "\nNAMESPACE\tWORKLOAD\tCONTAINER\tDECLARED JAVA\tUPSTREAM FIX\tPODS\tVBMI PODS\tCPU UNKNOWN\tMITIGATION DECLARED")
		for _, row := range report.Findings {
			fix := row.FixedIn
			if fix == "" {
				fix = "Check vendor"
			}
			workload := checkTableValue(row.Workload.Kind + "/" + row.Workload.Name)
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%d\t%d\t%d\t%d\n", row.Workload.Namespace, workload,
				row.Container, row.Version, fix, row.Pods, row.VBMIPods, row.UnknownCPUPods, row.MitigationDeclaredPods)
		}
		if report.FindingGroups > len(report.Findings) {
			fmt.Fprintf(w, "Showing %d of %d matching groups\n", len(report.Findings), report.FindingGroups)
		}
	}
	if len(report.Unassessed) > 0 {
		fmt.Fprintln(w, "\nUnassessed containers")
		for _, reason := range slices.Sorted(maps.Keys(report.Unassessed)) {
			fmt.Fprintf(w, "%d\t%s\n", report.Unassessed[reason], checkReasonLabel(reason))
		}
		fmt.Fprintln(w, "\nNAMESPACE\tWORKLOAD\tCONTAINER\tIMAGE\tREASON\tPODS")
		for _, row := range report.UnassessedRows {
			workload := checkTableValue(row.Workload.Kind + "/" + row.Workload.Name)
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%d\n", row.Workload.Namespace, workload,
				row.Container, checkTableValue(row.Image), checkReasonLabel(row.Reason), row.Pods)
		}
		if report.UnassessedGroups > len(report.UnassessedRows) {
			fmt.Fprintf(w, "Showing %d of %d unassessed groups\n", len(report.UnassessedRows), report.UnassessedGroups)
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	_, err := table.WriteTo(out)
	return err
}

func checkReasonLabel(reason string) string {
	switch reason {
	case "docker_hub_limit":
		return "Docker Hub lookup limit reached"
	case "java_home_declared":
		return "Image sets JAVA_HOME. Java version unknown"
	case "no_java_declaration":
		return "Image declares no Java version"
	default:
		return strings.ReplaceAll(reason, "_", " ")
	}
}

func checkTableValue(text string) string {
	quoted := strconv.Quote(text)
	if quoted[1:len(quoted)-1] == text {
		return text
	}
	return quoted
}
