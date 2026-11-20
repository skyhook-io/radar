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
	"os"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/term"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
	java4ShellTableGapLimit  = 10
	java4ShellRegistryDocs   = "https://github.com/skyhook-io/radar/blob/main/docs/authentication.md#image-inspection"
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
		fmt.Fprintln(fs.Output(), "Usage: radar check java4shell [-n NAMESPACE | -A] [-o table|json] [--details] [--fail-on-match]")
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
	details := fs.Bool("details", false, "Show image and node details in the table")
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
		err = writeJava4ShellCheck(out, output, result, *details)
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
		if namespace == "" && apierrors.IsForbidden(listErr) {
			return images.Java4ShellReport{}, fmt.Errorf("list Pods across all namespaces: %w. Use -n NAMESPACE where you can list Pods", listErr)
		}
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
	terminal, isFile := progress.(*os.File)
	updateProgress, finishProgress := checkImageProgress(progress, isFile && term.IsTerminal(int(terminal.Fd())) && os.Getenv("TERM") != "dumb")
	defer finishProgress()
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
	}, updateProgress)
	if err != nil {
		return images.Java4ShellReport{}, err
	}
	if ctx.Err() != nil {
		return images.Java4ShellReport{}, ctx.Err()
	}
	return report, nil
}

func checkImageProgress(out io.Writer, interactive bool) (update func(int, int), finish func()) {
	interval := 15 * time.Second
	if interactive {
		interval = time.Second
	}
	var last time.Time
	return func(done, total int) {
			if total == 0 || (done != total && time.Since(last) < interval) {
				return
			}
			if interactive {
				fmt.Fprintf(out, "\rProcessed %d of %d image groups", done, total)
			} else {
				fmt.Fprintf(out, "Processed %d of %d image groups\n", done, total)
			}
			last = time.Now()
		}, func() {
			if interactive && !last.IsZero() {
				fmt.Fprintln(out)
			}
		}
}

func writeJava4ShellCheck(out io.Writer, output string, report images.Java4ShellReport, details bool) error {
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
	scope := "all namespaces"
	if report.Coverage.Scope.Namespaces != nil {
		scope = "namespace " + report.Coverage.Scope.Namespaces.Names[0]
	}
	fmt.Fprintf(&table, "Java4Shell (%s) · %s\n", images.Java4ShellCVE, scope)
	unassessed := 0
	for _, count := range report.Unassessed {
		unassessed += count
	}
	noDeclaration := report.Unassessed["no_java_declaration"]
	gaps := unassessed - noDeclaration
	outside := report.Containers - report.Matches - unassessed
	if report.Containers == 0 {
		fmt.Fprintln(&table, "No running containers in the requested scope")
	} else {
		switch {
		case report.Matches > 0:
			fmt.Fprintf(&table, "Affected Java declarations in %d of %d assessed containers.\n", report.Matches, report.Matches+outside)
		case noDeclaration == report.Containers:
			fmt.Fprintln(&table, "None of the running images declared a Java version.")
		case outside == 0:
			fmt.Fprintln(&table, "No Java versions could be assessed.")
		default:
			fmt.Fprintf(&table, "No affected Java declarations among assessed containers (%d).\n", outside)
		}
		fmt.Fprintf(&table, "Running containers: %d · Outside affected ranges: %d\n", report.Containers, outside)
		if unassessed > 0 {
			fmt.Fprintf(&table, "No Java version declared: %d · Assessment gaps: %d\n", noDeclaration, gaps)
		}
	}
	for _, major := range slices.Sorted(maps.Keys(report.UnaffectedByMajor)) {
		fmt.Fprintf(&table, "Java %d declarations outside affected ranges: %d\n", major, report.UnaffectedByMajor[major])
	}
	if len(report.Findings) > 0 {
		fmt.Fprintln(&table, "\nAffected declarations")
		w := tabwriter.NewWriter(&table, 0, 0, 2, ' ', 0)
		fmt.Fprint(w, "NAMESPACE\tWORKLOAD\tCONTAINER\t")
		if details {
			fmt.Fprint(w, "IMAGE\t")
		}
		fmt.Fprint(w, "JAVA\tADVISORY FIX\tPODS")
		if details {
			fmt.Fprint(w, "\tAVX-512 VBMI\tCPU UNKNOWN\tMITIGATION FLAG")
		}
		fmt.Fprintln(w)
		for _, row := range report.Findings {
			fix := row.FixedIn
			if fix == "" {
				fix = "Check vendor"
			}
			workload := checkTableValue(row.Workload.Kind + "/" + row.Workload.Name)
			fmt.Fprintf(w, "%s\t%s\t%s\t", row.Workload.Namespace, workload, row.Container)
			if details {
				fmt.Fprintf(w, "%s\t", checkTableValue(row.Image))
			}
			fmt.Fprintf(w, "%s\t%s\t%d", row.Version, fix, row.Pods)
			if details {
				fmt.Fprintf(w, "\t%d\t%d\t%d", row.VBMIPods, row.UnknownCPUPods, row.MitigationDeclaredPods)
			} else {
				if row.VBMIPods > 0 {
					fmt.Fprintf(w, " · %d with AVX-512 VBMI", row.VBMIPods)
				}
				if row.UnknownCPUPods > 0 {
					fmt.Fprintf(w, " · %d CPU unknown", row.UnknownCPUPods)
				}
			}
			fmt.Fprintln(w)
		}
		_ = w.Flush()
		if report.FindingGroups > len(report.Findings) {
			fmt.Fprintf(&table, "Showing %d of %d matching groups. Narrow the scope with -n to see more.\n", len(report.Findings), report.FindingGroups)
		}
		fmt.Fprintln(&table, "Confirm the installed Java version and the fix from its vendor.")
		if details {
			fmt.Fprintln(&table, "Node capability and mitigation flag columns count Pods. Flags are read from Pod specs.")
		}
	}
	if gaps > 0 {
		fmt.Fprintln(&table, "\nAssessment gaps")
		for _, reason := range slices.Sorted(maps.Keys(report.Unassessed)) {
			if reason != "no_java_declaration" {
				fmt.Fprintf(&table, "  %s: %d\n", checkReasonLabel(reason), report.Unassessed[reason])
			}
		}
		if report.Unassessed[images.StatusAuthRequired] > 0 {
			fmt.Fprintf(&table, "Configure registry credentials for Radar, then rerun. See registry setup for Amazon ECR and other registries.\n  %s\n", java4ShellRegistryDocs)
			fmt.Fprintln(&table, "For private ECR, set RADAR_JAVA4SHELL_ECR_REGISTRIES and use an AWS identity with read access.")
		}
		if report.Unassessed["docker_hub_limit"]+report.Unassessed[images.StatusNotChecked] > 0 {
			if report.Coverage.Scope.Namespaces == nil {
				fmt.Fprintln(&table, "Narrow the scan with -n NAMESPACE to retry images skipped because of a limit.")
			} else {
				fmt.Fprintln(&table, "Check a single workload in Radar to reduce registry lookups.")
			}
		}
		if report.Unassessed["java_home_declared"] > 0 {
			fmt.Fprintln(&table, "Verify the installed Java version for images that set JAVA_HOME.")
		}
	}
	writeJava4ShellAssessmentRows(&table, report, details)
	if !details && (len(report.Findings) > 0 || unassessed > 0) {
		fmt.Fprintln(&table, "\nUse --details for image references, node evidence and additional rows.")
	}
	_, err := table.WriteTo(out)
	return err
}

func writeJava4ShellAssessmentRows(out *bytes.Buffer, report images.Java4ShellReport, details bool) {
	rows := report.UnassessedRows
	if !details {
		rows = make([]images.Java4ShellUnassessed, 0, len(report.UnassessedRows))
		for _, row := range report.UnassessedRows {
			if row.Reason != "no_java_declaration" {
				rows = append(rows, row)
			}
		}
	}
	if len(rows) == 0 {
		return
	}
	shown := len(rows)
	if !details {
		shown = min(shown, java4ShellTableGapLimit)
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if details {
		fmt.Fprintln(out, "\nContainer assessment details")
	} else {
		fmt.Fprintln(out)
	}
	fmt.Fprint(w, "NAMESPACE\tWORKLOAD\tCONTAINER\t")
	if details {
		fmt.Fprint(w, "IMAGE\t")
	}
	fmt.Fprintln(w, "REASON\tPODS")
	for _, row := range rows[:shown] {
		workload := checkTableValue(row.Workload.Kind + "/" + row.Workload.Name)
		fmt.Fprintf(w, "%s\t%s\t%s\t", row.Workload.Namespace, workload, row.Container)
		if details {
			fmt.Fprintf(w, "%s\t", checkTableValue(row.Image))
		}
		fmt.Fprintf(w, "%s\t%d\n", checkReasonLabel(row.Reason), row.Pods)
	}
	_ = w.Flush()
	if details && report.UnassessedGroups > shown {
		fmt.Fprintf(out, "Showing %d of %d unassessed groups. Narrow the scope with -n to see more.\n", shown, report.UnassessedGroups)
	}
	if !details && (shown < len(rows) || len(rows) == len(report.UnassessedRows) && report.UnassessedGroups > shown) {
		fmt.Fprintf(out, "Showing the first %d workload groups with gaps.\n", shown)
	}
}

func checkReasonLabel(reason string) string {
	switch reason {
	case images.StatusAuthRequired:
		return "Registry authentication required"
	case images.StatusNotChecked:
		return "Scan limit reached"
	case "image_identity_unknown":
		return "Running image digest unavailable"
	case "platform_unknown":
		return "Image platform unknown"
	case "version_unknown":
		return "Unrecognized Java version"
	case images.StatusNotFound:
		return "Image missing or private"
	case images.StatusRateLimited:
		return "Registry rate limit"
	case images.StatusBlocked:
		return "Registry address blocked"
	case images.StatusError:
		return "Registry read failed"
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
