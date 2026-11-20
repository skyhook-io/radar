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

	"github.com/google/go-containerregistry/pkg/name"
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

	"github.com/skyhook-io/radar/internal/cliui"
	"github.com/skyhook-io/radar/internal/images"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/resourceapi"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

const (
	java4ShellPodListBudget   = 30 * time.Second
	java4ShellMetadataBudget  = 5 * time.Second
	java4ShellPodLimit        = 50000
	java4ShellTableGapLimit   = 10
	java4ShellTableMatchLimit = 20
	java4ShellHintHostLimit   = 4
	java4ShellRegistryDocs    = "https://github.com/skyhook-io/radar/blob/main/docs/authentication.md#image-inspection"
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
		fmt.Fprintln(fs.Output(), "       radar check java4shell --from-report FILE [--details | -o json]")
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
	details := fs.Bool("details", false, "Show all reported rows with image and node details")
	dockerHubAuth := fs.Bool("docker-hub-auth", false, "Use your local Docker Hub login and its pull quota without Radar's 24-group cap")
	saveReport := fs.String("save-report", "", "Choose the JSON report path (table scans save to a temporary file by default)")
	fromReport := fs.String("from-report", "", "Open a saved report without contacting Kubernetes or registries")
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

	var report java4ShellCLIReport
	savedPath := *fromReport
	var err error
	if *fromReport != "" {
		if *saveReport != "" || *dockerHubAuth || namespace != "" || allNamespaces || *contextName != "" || *kubeconfig != "" {
			fmt.Fprintln(errOut, "radar check java4shell: --from-report cannot be combined with scan or save flags")
			return 1
		}
		report, err = readJava4ShellReport(*fromReport)
	} else {
		var file *os.File
		if *saveReport != "" || output == "table" {
			file, err = openJava4ShellReport(*saveReport)
			if err != nil {
				if *saveReport != "" {
					fmt.Fprintf(errOut, "radar check java4shell: %v\n", err)
					return 1
				}
				fmt.Fprintf(errOut, "Could not create a temporary report. Printing all rows instead: %v\n", err)
				*details = true
			}
		}
		if file != nil {
			savedPath = file.Name()
			defer func() {
				if file != nil {
					_ = file.Close()
					_ = os.Remove(savedPath)
				}
			}()
		}
		fmt.Fprintln(errOut, "Loading Kubernetes configuration…")
		result, scannedContext, scanErr := runJava4ShellCheck(*kubeconfig, *contextName, namespace, allNamespaces, *dockerHubAuth, errOut)
		report = java4ShellCLIReport{
			CVE: images.Java4ShellCVE, CheckedAt: time.Now().UTC(), Context: scannedContext,
			DockerHubLogin: *dockerHubAuth, Java4ShellReport: result,
		}
		err = scanErr
		if err == nil && output == "table" && !*dockerHubAuth {
			report.dockerHubRetry = java4ShellDockerHubRetry(report, *kubeconfig, *saveReport, *details, *failOnMatch)
		}
		if err == nil && file != nil {
			err = errors.Join(report.write(file, "json", false, ""), file.Close())
			if err == nil {
				file = nil
			} else {
				// The scan finished. Keep its rows available even if the file write failed.
				err = errors.Join(err, report.write(out, output, true, ""))
			}
		}
	}
	if err == nil {
		err = report.write(out, output, *details, savedPath)
	}
	if err != nil {
		fmt.Fprintf(errOut, "radar check java4shell: %v\n", err)
		return 1
	}
	if *failOnMatch && report.Matches > 0 {
		return 2
	}
	return 0
}

func runJava4ShellCheck(kubeconfig, contextName, namespace string, allNamespaces, dockerHubAuth bool, progress io.Writer) (images.Java4ShellReport, string, error) {
	if kubeconfig != "" {
		path, err := k8s.NormalizeKubeconfigPath(kubeconfig)
		if err != nil {
			return images.Java4ShellReport{}, "", err
		}
		kubeconfig = path
	}
	loader := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(connectLoadingRules(kubeconfig),
		&clientcmd.ConfigOverrides{CurrentContext: contextName})
	cfg, err := loader.ClientConfig()
	if err != nil {
		return images.Java4ShellReport{}, "", fmt.Errorf("load Kubernetes configuration: %w", err)
	}
	if namespace == "" && !allNamespaces {
		namespace, _, err = loader.Namespace()
		if err != nil {
			return images.Java4ShellReport{}, "", fmt.Errorf("read context namespace: %w", err)
		}
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return images.Java4ShellReport{}, "", fmt.Errorf("create Kubernetes client: %w", err)
	}

	if contextName == "" {
		if config, err := loader.RawConfig(); err == nil {
			contextName = config.CurrentContext
		}
	}
	ctx, stop := signalContext()
	defer stop()
	inventory := images.NewCLIJava4ShellInventory()
	if dockerHubAuth {
		if err := inventory.UseDockerHubLogin(ctx); err != nil {
			return images.Java4ShellReport{}, "", err
		}
	}
	report, err := checkJava4Shell(ctx, client, inventory, namespace, progress)
	return report, contextName, err
}

func checkJava4Shell(ctx context.Context, client kubernetes.Interface, inventory *images.RuntimeInventory, namespace string, progress io.Writer) (images.Java4ShellReport, error) {
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
	report.Java4ShellResult, err = inventory.ScanJava4Shell(ctx, pods, func(ctx context.Context, pod *corev1.Pod) resourceid.Ref {
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
				if done == total {
					fmt.Fprintln(out)
					last = time.Time{}
					return
				}
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

type java4ShellCLIReport struct {
	CVE            string    `json:"cve"`
	CheckedAt      time.Time `json:"checkedAt"`
	Context        string    `json:"context,omitempty"`
	DockerHubLogin bool      `json:"dockerHubLogin,omitempty"`
	images.Java4ShellReport

	dockerHubRetry string
}

func readJava4ShellReport(path string) (java4ShellCLIReport, error) {
	var report java4ShellCLIReport
	file, err := os.Open(path)
	if err != nil {
		return report, err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&report); err != nil {
		return report, fmt.Errorf("read report %q: %w", path, err)
	}
	if report.CVE != images.Java4ShellCVE || report.CheckedAt.IsZero() {
		return report, fmt.Errorf("%q is not a Java4Shell report", path)
	}
	return report, nil
}

func openJava4ShellReport(path string) (*os.File, error) {
	if path == "" {
		return os.CreateTemp("", "radar-java4shell-*.json")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("report %q already exists; choose a new filename", path)
	}
	return file, err
}

func (snapshot java4ShellCLIReport) write(out io.Writer, output string, details bool, savedPath string) error {
	if output == "json" {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(snapshot)
	}
	report := snapshot.Java4ShellReport

	var table bytes.Buffer
	style := cliui.New(out)
	scope := "all namespaces"
	if namespaces := report.Coverage.Scope.Namespaces; namespaces != nil && len(namespaces.Names) > 0 {
		scope = "namespace " + checkTableValue(strings.Join(namespaces.Names, ", "))
	}
	fmt.Fprintf(&table, "\n%s", style.Bold("Java4Shell ("+images.Java4ShellCVE+")"))
	if snapshot.Context != "" {
		fmt.Fprintf(&table, " · context %s", checkTableValue(snapshot.Context))
	}
	fmt.Fprintf(&table, " · %s\n\n", scope)
	unassessed := 0
	for _, count := range report.Unassessed {
		unassessed += count
	}
	noDeclaration := report.Unassessed["no_java_declaration"]
	gaps := unassessed - noDeclaration
	outside := report.Containers - report.Matches - unassessed
	if len(report.Findings) > 0 {
		title := fmt.Sprintf("Affected Java versions declared in %d containers", report.Matches)
		if report.Matches == 1 {
			title = "Affected Java version declared in 1 container"
		}
		vbmi, cpuUnknown, priorityRows := 0, 0, 0
		var firstPriority images.Java4ShellFinding
		for _, row := range report.Findings {
			if row.VBMIPods > 0 {
				if priorityRows == 0 {
					firstPriority = row
				}
				priorityRows++
				vbmi += row.VBMIPods
			}
			cpuUnknown += row.UnknownCPUPods
		}
		if vbmi > 0 {
			priority := fmt.Sprintf("PATCH FIRST · %d %s with an affected Java declaration and AVX-512 VBMI", vbmi, java4ShellContainerUnit(vbmi))
			fmt.Fprintln(&table, style.Apply(cliui.Bold+cliui.Red, priority))
			if priorityRows == 1 {
				row := firstPriority
				fmt.Fprintf(&table, "%s/%s/%s · container %s\n", checkTableValue(row.Workload.Namespace), checkTableValue(row.Workload.Kind), checkTableValue(row.Workload.Name), checkTableValue(row.Container))
			}
			fmt.Fprintln(&table, "\n"+style.Bold(title))
		} else {
			fmt.Fprintln(&table, style.Apply(cliui.Bold+cliui.Amber, title))
		}
		if cpuUnknown > 0 {
			fmt.Fprintf(&table, "CPU capability unknown for %d matching %s. Check the node CPU flags.\n", cpuUnknown, java4ShellContainerUnit(cpuUnknown))
		}
		if vbmi == 0 && cpuUnknown == 0 && report.FindingGroups <= len(report.Findings) {
			fmt.Fprintln(&table, "Current CPU evidence points away from exposure. The vulnerable path needs AVX-512 VBMI.")
		}
		fmt.Fprintln(&table)
		w := tabwriter.NewWriter(&table, 0, 0, 2, ' ', 0)
		fmt.Fprint(w, "NAMESPACE\tWORKLOAD\tCONTAINER\t")
		if details {
			fmt.Fprint(w, "IMAGE\t")
		}
		fmt.Fprint(w, "DECLARED JAVA\tADVISORY FIX\tPODS")
		if details {
			fmt.Fprint(w, "\tAVX-512 VBMI\tCPU UNKNOWN\tVBMI ABSENT (INFERRED)\tNON-X86\tMITIGATION FLAG")
		} else {
			fmt.Fprint(w, "\tCPU EVIDENCE")
		}
		fmt.Fprintln(w)
		findings := report.Findings
		if !details {
			findings = findings[:min(len(findings), java4ShellTableMatchLimit)]
		}
		for _, row := range findings {
			fix := row.FixedIn
			if fix == "" {
				fix = "Check vendor"
			}
			workload := checkTableValue(row.Workload.Kind + "/" + row.Workload.Name)
			fmt.Fprintf(w, "%s\t%s\t%s\t", checkTableValue(row.Workload.Namespace), workload, checkTableValue(row.Container))
			if details {
				fmt.Fprintf(w, "%s\t", checkTableValue(row.Image))
			}
			fmt.Fprintf(w, "%s\t%s\t%d", checkTableValue(row.Version), checkTableValue(fix), row.Pods)
			inferredNoVBMI := 0
			for _, count := range row.NoVBMIByInstanceType {
				inferredNoVBMI += count
			}
			nonX86 := row.Pods - row.VBMIPods - row.UnknownCPUPods - inferredNoVBMI
			if details {
				fmt.Fprintf(w, "\t%d\t%d\t%d", row.VBMIPods, row.UnknownCPUPods, inferredNoVBMI)
				if inferredNoVBMI > 0 {
					fmt.Fprintf(w, " (%s)", checkTableValue(strings.Join(slices.Sorted(maps.Keys(row.NoVBMIByInstanceType)), ", ")))
				}
				fmt.Fprintf(w, "\t%d\t%d", nonX86, row.MitigationDeclaredPods)
			} else {
				var evidence []string
				if row.VBMIPods > 0 {
					evidence = append(evidence, java4ShellCPUCount(row.Pods, row.VBMIPods, "AVX-512 VBMI reported"))
				}
				if row.UnknownCPUPods > 0 {
					evidence = append(evidence, java4ShellCPUCount(row.Pods, row.UnknownCPUPods, "CPU capability unknown"))
				}
				if inferredNoVBMI > 0 {
					source := "instance types"
					if len(row.NoVBMIByInstanceType) == 1 {
						for instanceType := range row.NoVBMIByInstanceType {
							source = instanceType
						}
					}
					evidence = append(evidence, java4ShellCPUCount(row.Pods, inferredNoVBMI, "VBMI absent (inferred from "+checkTableValue(source)+")"))
				}
				if nonX86 > 0 {
					evidence = append(evidence, java4ShellCPUCount(row.Pods, nonX86, "non-x86"))
				}
				fmt.Fprintf(w, "\t%s", strings.Join(evidence, " · "))
			}
			fmt.Fprintln(w)
		}
		_ = w.Flush()
		if len(findings) < len(report.Findings) {
			fmt.Fprintf(&table, "Showing %d of %d matching rows. Use --details for all of them.\n", len(findings), len(report.Findings))
		} else if report.FindingGroups > len(report.Findings) {
			fmt.Fprintf(&table, "This report contains %d of %d matching rows.\n", len(report.Findings), report.FindingGroups)
		}
		fmt.Fprintln(&table, "Confirm the installed Java version and check its vendor's advisory.")
		if details {
			fmt.Fprintln(&table, "Node capability and mitigation flag columns count Pods. Flags are read from Pod specs.")
		}
	}
	if report.Matches > 0 {
		fmt.Fprintln(&table, "\n"+style.Bold("Coverage"))
	}
	if report.Containers == 0 {
		fmt.Fprintln(&table, "No running containers in the requested scope.")
		fmt.Fprintln(&table, "Check the Kubernetes context and namespace if you expected running Pods.")
	} else {
		switch {
		case report.Matches > 0:
		case noDeclaration == report.Containers:
			fmt.Fprintln(&table, "None of the running images declared a Java version.")
		case outside == 0:
			fmt.Fprintln(&table, "No Java versions could be assessed.")
		default:
			fmt.Fprintf(&table, "No affected Java declarations among assessed containers (%d).\n", outside)
		}
		fmt.Fprintf(&table, "Running containers %d · Assessed %d · Outside affected ranges %d\n", report.Containers, report.Matches+outside, outside)
		if unassessed > 0 {
			fmt.Fprintf(&table, "%d without a Java version in image metadata · %d could not be checked\n", noDeclaration, gaps)
		}
		if noDeclaration > 0 {
			fmt.Fprintln(&table, "For images with no Java version in their metadata, verify the installed version in workloads that use Java.")
		}
	}
	for _, major := range slices.Sorted(maps.Keys(report.UnaffectedByMajor)) {
		fmt.Fprintf(&table, "Java %d declarations outside affected ranges: %d\n", major, report.UnaffectedByMajor[major])
	}
	if gaps > 0 {
		fmt.Fprintf(&table, "\n%s\n", style.Tone(cliui.Attention, "Could not check · next steps"))
		hosts := report.RateLimitedRegistries
		dockerHubOnly := len(hosts) == 1 && hosts[0] == name.DefaultRegistry
		dockerHubSkips := report.Unassessed["docker_hub_limit"]
		if dockerHubOnly || dockerHubSkips > 0 {
			throttled := 0
			if dockerHubOnly {
				throttled = report.Unassessed[images.StatusRateLimited]
			}
			snapshot.writeDockerHubRecovery(&table, style, throttled, dockerHubSkips)
		}
		for _, reason := range slices.Sorted(maps.Keys(report.Unassessed)) {
			if reason == "no_java_declaration" || reason == "docker_hub_limit" || (reason == images.StatusRateLimited && dockerHubOnly) {
				continue
			}
			count := report.Unassessed[reason]
			fmt.Fprintf(&table, "\n  %s · %d %s\n", style.Bold(checkTableValue(checkReasonLabel(reason))), count, java4ShellContainerUnit(count))
			switch reason {
			case images.StatusAuthRequired:
				writeJava4ShellAuthHint(&table, report.AuthRequiredRegistries, snapshot.DockerHubLogin)
			case images.StatusRateLimited:
				if len(hosts) > 0 {
					displayHosts := slices.Clone(hosts[:min(len(hosts), java4ShellHintHostLimit)])
					for i, host := range displayHosts {
						if host == name.DefaultRegistry {
							displayHosts[i] = "Docker Hub (" + host + ")"
						}
					}
					fmt.Fprintf(&table, "    HTTP 429 from %s", checkTableValue(strings.Join(displayHosts, ", ")))
					if len(hosts) > java4ShellHintHostLimit {
						fmt.Fprint(&table, " and others")
					}
					fmt.Fprintln(&table, ".")
				}
				if slices.Contains(hosts, name.DefaultRegistry) && dockerHubSkips == 0 {
					snapshot.writeDockerHubRetry(&table)
				}
				if slices.Contains(hosts, images.PublicECRRegistry) {
					fmt.Fprintln(&table, "    Amazon ECR Public allows one anonymous pull per second. Radar spaces these lookups apart.")
				}
				wait := "Wait for the registry quota to reset"
				if len(hosts) == 1 && hosts[0] == images.PublicECRRegistry {
					wait = "Wait a few seconds"
				} else {
					fmt.Fprintln(&table, "    The reset time is unknown. Check your registry's quota.")
				}
				if report.Coverage.Scope.Namespaces == nil {
					fmt.Fprintf(&table, "    %s, then retry a single affected namespace.\n", wait)
					fmt.Fprintln(&table, "    "+java4ShellNarrowerScope(report, reason))
				} else {
					fmt.Fprintf(&table, "    %s, then rerun this namespace.\n", wait)
				}
				fmt.Fprintln(&table, "    If throttling continues, verify Java directly in the affected workloads.")

			case images.StatusNotChecked:
				fmt.Fprintln(&table, "    "+java4ShellNarrowerScope(report, reason))
			case "java_home_declared":
				fmt.Fprintln(&table, "    Verify the installed Java version for images that set JAVA_HOME.")
			case "image_identity_unknown":
				fmt.Fprintln(&table, "    Radar could not identify the exact running image from Kubernetes Pod status.")
				fmt.Fprintln(&table, "    Find the workloads below or in the full report. Verify the installed version in containers that use Java.")
			case "version_unknown":
				fmt.Fprintln(&table, "    If these workloads use Java, confirm the installed version directly.")
			case "platform_unknown":
				fmt.Fprintln(&table, "    Confirm the installed Java version on the Pod's actual platform.")
			case images.StatusNotFound:
				fmt.Fprintln(&table, "    Use --details to find the image. Check that the digest exists and your registry credentials can read it.")
			case images.StatusBlocked:
				fmt.Fprintln(&table, "    Use --details to check the image's registry address. Radar refused the connection.")
			case images.StatusError:
				fmt.Fprintln(&table, "    Check connectivity to the image registry, then retry the affected workload or namespace.")
			}
		}
	}
	writeJava4ShellAssessmentRows(&table, style, report, details)
	if report.Matches > 0 || report.Unassessed["image_identity_unknown"] > 0 ||
		report.Unassessed["java_home_declared"] > 0 || gaps > 0 && snapshot.DockerHubLogin && slices.Contains(report.RateLimitedRegistries, name.DefaultRegistry) {
		fmt.Fprintln(&table, "\nTo check installed Java in a workload that uses it:")
		fmt.Fprintf(&table, "    kubectl%s -n NAMESPACE exec POD -c CONTAINER -- java -version\n", java4ShellContextArg(snapshot.Context))
	}
	if savedPath != "" {
		fmt.Fprintf(&table, "\nReport file %s · checked %s\n", strconv.Quote(savedPath), snapshot.CheckedAt.UTC().Format(time.RFC3339))
		if !details && (len(report.Findings) > 0 || unassessed > 0) {
			fmt.Fprintf(&table, "See every row without a rescan:\n    %s check java4shell --from-report %s --details\n", radarCLIInvocation(), shellQuote(savedPath))
		}
	}
	_, err := table.WriteTo(out)
	return err
}

func (snapshot java4ShellCLIReport) writeDockerHubRecovery(out io.Writer, style cliui.Styler, throttled, skipped int) {
	fmt.Fprintf(out, "\n  %s · %d %s\n", style.Bold("Docker Hub"), throttled+skipped, java4ShellContainerUnit(throttled+skipped))
	if throttled > 0 {
		fmt.Fprintf(out, "    %d hit HTTP 429 from Docker Hub (index.docker.io).\n", throttled)
	}
	if skipped > 0 {
		fmt.Fprintf(out, "    %d skipped by Radar's 24-lookup cap for scans without a Docker Hub login.\n", skipped)
	}
	snapshot.writeDockerHubRetry(out)
	if throttled > 0 {
		fmt.Fprintln(out, "    Docker Hub uses a six-hour pull quota and a separate traffic limit.")
		fmt.Fprintln(out, "    Radar could not determine the reset time.")
		fmt.Fprintln(out, "    https://docs.docker.com/docker-hub/troubleshoot/")
	}
	if skipped > 0 {
		fmt.Fprintln(out, "    Without a Docker login, narrow the check. "+java4ShellNarrowerScope(snapshot.Java4ShellReport, "docker_hub_limit"))
	}
}

func (snapshot java4ShellCLIReport) writeDockerHubRetry(out io.Writer) {
	if snapshot.DockerHubLogin {
		fmt.Fprintln(out, "    Check your account quota, retry later, or verify Java directly.")
	} else if snapshot.dockerHubRetry != "" {
		fmt.Fprintln(out, "    Sign in and rerun:")
		fmt.Fprintln(out, "      docker login")
		fmt.Fprintln(out, "      "+snapshot.dockerHubRetry)
		if os.Getenv("AWS_PROFILE") != "" || os.Getenv(images.ECRRegistriesEnv) != "" {
			fmt.Fprintln(out, "    Keep the AWS environment settings from your original command.")
		}
	} else {
		fmt.Fprintln(out, "    Run docker login, then rerun the scan with --docker-hub-auth.")
	}
}

func java4ShellDockerHubRetry(snapshot java4ShellCLIReport, kubeconfig, savePath string, details, failOnMatch bool) string {
	command := radarCLIInvocation() + " check java4shell" + java4ShellContextArg(snapshot.Context)
	if kubeconfig != "" {
		command += " --kubeconfig " + shellQuote(kubeconfig)
	}
	if scope := snapshot.Coverage.Scope.Namespaces; scope != nil && len(scope.Names) == 1 {
		command += " -n " + shellQuote(scope.Names[0])
	} else {
		command += " -A"
	}
	if details {
		command += " --details"
	}
	if failOnMatch {
		command += " --fail-on-match"
	}
	command += " --docker-hub-auth"
	if savePath != "" {
		command += " --save-report " + shellQuote(savePath+".retry.json")
	}
	return command
}

func java4ShellContextArg(contextName string) string {
	if contextName == "" {
		return ""
	}
	return " --context " + shellQuote(contextName)
}

func radarCLIInvocation() string {
	return shellQuote(os.Args[0])
}

func java4ShellContainerUnit(count int) string {
	if count == 1 {
		return "container"
	}
	return "containers"
}

func java4ShellCPUCount(pods, count int, evidence string) string {
	if pods == 1 {
		return evidence
	}
	return fmt.Sprintf("%d %s", count, evidence)
}

func shellQuote(path string) string {
	if checkTableValue(path) != path {
		return "'REPLACE_ME'"
	}
	return "'" + strings.ReplaceAll(path, "'", "'\\''") + "'"
}

func java4ShellNarrowerScope(report images.Java4ShellReport, reason string) string {
	if report.Coverage.Scope.Namespaces != nil {
		return "Verify installed Java directly in any affected workload that uses it."
	}
	for _, row := range report.UnassessedRows {
		if row.Reason == reason {
			return "For example, use -n " + checkTableValue(row.Workload.Namespace) + " instead of -A."
		}
	}
	return "Replace -A with -n NAMESPACE."
}

func writeJava4ShellAuthHint(out io.Writer, registries []string, dockerHubLogin bool) {
	var ecrHosts, otherHosts []string
	for _, host := range registries {
		if images.IsPrivateECRRegistry(host) {
			ecrHosts = append(ecrHosts, host)
		} else if host == name.DefaultRegistry {
			if dockerHubLogin {
				fmt.Fprintln(out, "    Run docker login again and verify the account has pull access to the affected Docker Hub repository.")
			} else {
				fmt.Fprintln(out, "    For Docker Hub, run docker login and rerun with --docker-hub-auth.")
			}
		} else {
			otherHosts = append(otherHosts, host)
		}
	}
	if len(ecrHosts) > 0 {
		configured := strings.TrimSpace(os.Getenv(images.ECRRegistriesEnv))
		configuredHosts := strings.Split(configured, ",")
		var unconfigured []string
		for _, host := range ecrHosts {
			if !slices.ContainsFunc(configuredHosts, func(entry string) bool { return strings.TrimSpace(entry) == host }) {
				unconfigured = append(unconfigured, host)
			}
		}
		if len(unconfigured) != len(ecrHosts) {
			fmt.Fprintln(out, "    For ECR hosts already configured, verify the local AWS profile is signed in and can pull the images.")
		}
		if len(unconfigured) > 0 {
			fmt.Fprintln(out, "    Private ECR needs a local AWS identity with pull access. Replace your-profile and rerun.")
			fmt.Fprintln(out, "      export AWS_PROFILE=your-profile")
			hosts := strings.Join(unconfigured[:min(len(unconfigured), java4ShellHintHostLimit)], ",")
			if configured != "" {
				fmt.Fprintf(out, "      export %[1]s=\"$%[1]s,%[2]s\"\n", images.ECRRegistriesEnv, hosts)
			} else {
				fmt.Fprintf(out, "      export %s=%s\n", images.ECRRegistriesEnv, hosts)
			}
			if len(unconfigured) > java4ShellHintHostLimit {
				fmt.Fprintln(out, "    Add other ECR registry hosts from --details, separated by commas.")
			}
		}
		fmt.Fprintln(out, "    If the profile uses SSO, run aws sso login --profile your-profile first.")
	}
	if len(otherHosts) > 0 {
		fmt.Fprintln(out, "    For these registries, run docker login REGISTRY and rerun the same check.")
		fmt.Fprintf(out, "      %s\n", checkTableValue(strings.Join(otherHosts[:min(len(otherHosts), java4ShellHintHostLimit)], ", ")))
	}
	if len(otherHosts) > java4ShellHintHostLimit || len(registries) == 0 {
		fmt.Fprintln(out, "    Use --details to see the image registry.")
	}
	fmt.Fprintln(out, "    Registry setup", java4ShellRegistryDocs)
}

func writeJava4ShellAssessmentRows(out *bytes.Buffer, style cliui.Styler, report images.Java4ShellReport, details bool) {
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
	totalRows := len(rows)
	if !details && totalRows > java4ShellTableGapLimit {
		// Show missing image identities even when a registry failure dominates.
		digestRows := make([]images.Java4ShellUnassessed, 0, 4)
		for _, row := range rows {
			if row.Reason == "image_identity_unknown" {
				digestRows = append(digestRows, row)
				if len(digestRows) == cap(digestRows) {
					break
				}
			}
		}
		preview := slices.Clone(digestRows)
		for _, row := range rows {
			if row.Reason == "image_identity_unknown" && slices.Contains(digestRows, row) {
				continue
			}
			preview = append(preview, row)
			if len(preview) == java4ShellTableGapLimit {
				break
			}
		}
		rows = preview
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	heading := "Containers Radar could not check"
	if details {
		heading = "Container assessment details"
	}
	fmt.Fprintln(out, "\n"+style.Bold(heading))
	fmt.Fprint(w, "NAMESPACE\tWORKLOAD\tCONTAINER\t")
	if details {
		fmt.Fprint(w, "IMAGE\t")
	}
	fmt.Fprintln(w, "REASON\tPODS")
	for _, row := range rows {
		workload := checkTableValue(row.Workload.Kind + "/" + row.Workload.Name)
		fmt.Fprintf(w, "%s\t%s\t%s\t", checkTableValue(row.Workload.Namespace), workload, checkTableValue(row.Container))
		if details {
			fmt.Fprintf(w, "%s\t", checkTableValue(row.Image))
		}
		fmt.Fprintf(w, "%s\t%d\n", checkTableValue(checkReasonLabel(row.Reason)), row.Pods)
	}
	_ = w.Flush()
	if details && report.UnassessedGroups > len(rows) {
		fmt.Fprintf(out, "This report contains %d of %d rows.\n", len(rows), report.UnassessedGroups)
	}
	if !details && len(rows) < totalRows {
		fmt.Fprintf(out, "Showing %d of %d rows. Replicas with the same workload, container, image and reason share a row.\n", len(rows), totalRows)
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
		return "Radar's Docker Hub lookup cap reached"
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
