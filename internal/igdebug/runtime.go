package igdebug

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/inspektor-gadget/inspektor-gadget/pkg/datasource"
	igjson "github.com/inspektor-gadget/inspektor-gadget/pkg/datasource/formatters/json"
	gadgetcontext "github.com/inspektor-gadget/inspektor-gadget/pkg/gadget-context"
	"github.com/inspektor-gadget/inspektor-gadget/pkg/operators"
	"github.com/inspektor-gadget/inspektor-gadget/pkg/operators/simple"
	grpcruntime "github.com/inspektor-gadget/inspektor-gadget/pkg/runtime/grpc"

	radark8s "github.com/skyhook-io/radar/internal/k8s"
	domain "github.com/skyhook-io/radar/pkg/igdebug"
)

const (
	processDigest    = "sha256:a436639d529d8d8924d74801ad25832e0469d1bddf73c206a042c421ffa1cf1a"
	socketDigest     = "sha256:85d76e100f8124d806eeebab043f0f2e4d9d116ff9683cc6b1f1b0018cc1d566"
	dnsDigest        = "sha256:24e93e604421ca095e4f9116c71514cc0d326cef89fb4c5d83a885bc985af935"
	defaultRegistry  = "ghcr.io/inspektor-gadget/gadget"
	operatorPriority = 50_000
	snapshotTimeout  = 30 * time.Second
)

type runtimeExecutor struct {
	config          *rest.Config
	gadgetNamespace string
	registry        string
	startupTimeout  time.Duration
}

func (e *runtimeExecutor) Execute(ctx context.Context, request domain.Request, reporter domain.Reporter) error {
	client, err := kubernetes.NewForConfig(e.config)
	if err != nil {
		return fmt.Errorf("create Kubernetes client: %w", err)
	}
	startupCtx, cancelStartup := context.WithTimeout(ctx, e.startupTimeout)
	target, err := resolveTarget(startupCtx, client, e.gadgetNamespace, request.Target)
	cancelStartup()
	if err != nil {
		return err
	}
	request.Target = target
	enricher := loadVisibleEnricher(ctx, client, target)

	runCtx, cancelRun := context.WithCancelCause(ctx)
	defer cancelRun(nil)
	go watchTarget(runCtx, client, target, reporter)
	started := make(chan struct{})
	completed := make(chan struct{})
	var startedOnce sync.Once
	var completedOnce sync.Once
	var resultPublished atomic.Bool
	markStarted := func() { startedOnce.Do(func() { close(started) }) }
	markCompleted := func() {
		resultPublished.Store(true)
		completedOnce.Do(func() { close(completed) })
	}
	go func() {
		timer := time.NewTimer(e.startupTimeout)
		defer timer.Stop()
		select {
		case <-started:
		case <-timer.C:
			cancelRun(errors.New("Inspektor Gadget startup timed out before instrumentation became ready"))
		case <-runCtx.Done():
		}
	}()
	if request.Aspect != domain.AspectDNS {
		go enforceSnapshotDeadline(runCtx, started, completed, snapshotTimeout, reporter, cancelRun)
	}

	var output operators.DataOperator
	switch request.Aspect {
	case domain.AspectProcesses, domain.AspectConnections:
		output = snapshotOperator(request.Aspect, target, enricher, reporter, markStarted, markCompleted)
	case domain.AspectDNS:
		output = dnsOperator(target, request.Duration, enricher, reporter, markStarted, markCompleted)
	default:
		return domain.ErrInvalidRequest
	}

	options := []gadgetcontext.Option{gadgetcontext.WithDataOperators(output)}
	if request.Aspect == domain.AspectDNS {
		options = append(options, gadgetcontext.WithTimeout(request.Duration))
	}
	gadgetCtx := gadgetcontext.New(runCtx, e.image(request.Aspect), options...)
	runtime := grpcruntime.New(grpcruntime.WithConnectUsingK8SProxy)
	runtime.SetRestConfig(e.config)
	globalParams := runtime.GlobalParamDescs().ToParams()
	if err := globalParams.Set(grpcruntime.ParamGadgetNamespace, e.gadgetNamespace); err != nil {
		return fmt.Errorf("set gadget namespace: %w", err)
	}
	if err := runtime.Init(globalParams); err != nil {
		return fmt.Errorf("initialize Inspektor Gadget runtime: %w", err)
	}
	defer runtime.Close()
	runtimeParams := runtime.ParamDescs().ToParams()
	if err := runtimeParams.Set(grpcruntime.ParamNode, target.Node); err != nil {
		return fmt.Errorf("set target node: %w", err)
	}
	params := map[string]string{
		"operator.KubeManager.namespace":     target.Namespace,
		"operator.KubeManager.podname":       target.Pod,
		"operator.KubeManager.containername": target.Container,
	}
	if err := runtime.RunGadget(gadgetCtx, runtimeParams, params); err != nil {
		if resultPublished.Load() {
			return nil
		}
		if cause := context.Cause(runCtx); cause != nil && !errors.Is(cause, context.Canceled) {
			return cause
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return ctx.Err()
		}
		return fmt.Errorf("run Inspektor Gadget: %w", err)
	}
	return nil
}

func enforceSnapshotDeadline(ctx context.Context, started, completed <-chan struct{}, timeout time.Duration, reporter domain.Reporter, cancel context.CancelCauseFunc) {
	select {
	case <-started:
	case <-completed:
		return
	case <-ctx.Done():
		return
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-completed:
	case <-timer.C:
		reason := "Inspektor Gadget snapshot timed out before returning data"
		reporter.Partial(reason)
		cancel(errors.New(reason))
	case <-ctx.Done():
	}
}

func (e *runtimeExecutor) image(aspect domain.Aspect) string {
	registry := e.registry
	if registry == "" {
		registry = defaultRegistry
	}
	switch aspect {
	case domain.AspectProcesses:
		return registry + "/snapshot_process@" + processDigest
	case domain.AspectConnections:
		return registry + "/snapshot_socket@" + socketDigest
	default:
		return registry + "/trace_dns@" + dnsDigest
	}
}

func resolveTarget(ctx context.Context, client kubernetes.Interface, gadgetNamespace string, requested domain.Target) (domain.Target, error) {
	pod, err := client.CoreV1().Pods(requested.Namespace).Get(ctx, requested.Pod, metav1.GetOptions{})
	if err != nil {
		return domain.Target{}, fmt.Errorf("get target Pod %s/%s: %w", requested.Namespace, requested.Pod, err)
	}
	if pod.Spec.NodeName == "" {
		return domain.Target{}, fmt.Errorf("Pod %s/%s is not scheduled to a node", requested.Namespace, requested.Pod)
	}
	containerName, containerID := runningContainer(pod, requested.Container)
	if containerID == "" {
		if requested.Container == "" {
			return domain.Target{}, fmt.Errorf("Pod %s/%s has no running containers", requested.Namespace, requested.Pod)
		}
		return domain.Target{}, fmt.Errorf("container %q is not running in Pod %s/%s", requested.Container, requested.Namespace, requested.Pod)
	}
	gadgetPods, err := client.CoreV1().Pods(gadgetNamespace).List(ctx, metav1.ListOptions{LabelSelector: "k8s-app=gadget", FieldSelector: "spec.nodeName=" + pod.Spec.NodeName})
	if err != nil {
		return domain.Target{}, fmt.Errorf("list Inspektor Gadget Pods on node %s: %w", pod.Spec.NodeName, err)
	}
	for _, gadgetPod := range gadgetPods.Items {
		if podReady(&gadgetPod) {
			return domain.Target{
				Namespace:   requested.Namespace,
				Pod:         requested.Pod,
				PodUID:      string(pod.UID),
				Node:        pod.Spec.NodeName,
				Container:   containerName,
				ContainerID: containerID,
			}, nil
		}
	}
	return domain.Target{}, fmt.Errorf("Inspektor Gadget Pod is not Ready on node %s", pod.Spec.NodeName)
}

func runningContainer(pod *corev1.Pod, requested string) (string, string) {
	statuses := make(map[string]corev1.ContainerStatus, len(pod.Status.ContainerStatuses))
	for _, status := range pod.Status.ContainerStatuses {
		statuses[status.Name] = status
	}
	containerNames := make([]string, 0, len(pod.Spec.Containers))
	if requested != "" {
		containerNames = append(containerNames, requested)
	} else if defaultName := radark8s.DefaultContainerName(pod); defaultName != "" {
		containerNames = append(containerNames, defaultName)
		for _, container := range pod.Spec.Containers {
			if container.Name != defaultName {
				containerNames = append(containerNames, container.Name)
			}
		}
	}
	for _, containerName := range containerNames {
		status, ok := statuses[containerName]
		if ok && status.State.Running != nil && status.ContainerID != "" {
			return containerName, trimRuntimePrefix(status.ContainerID)
		}
		if requested != "" {
			return "", ""
		}
	}
	return "", ""
}

func podReady(pod *corev1.Pod) bool {
	if pod.DeletionTimestamp != nil {
		return false
	}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

func trimRuntimePrefix(containerID string) string {
	if index := strings.Index(containerID, "://"); index >= 0 {
		return containerID[index+3:]
	}
	return containerID
}

func watchTarget(ctx context.Context, client kubernetes.Interface, target domain.Target, reporter domain.Reporter) {
	watchTargetEvery(ctx, client, target, reporter, time.Second)
}

func watchTargetEvery(ctx context.Context, client kubernetes.Interface, target domain.Target, reporter domain.Reporter, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	consecutiveErrors := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pod, err := client.CoreV1().Pods(target.Namespace).Get(ctx, target.Pod, metav1.GetOptions{})
			if err != nil {
				if apierrors.IsNotFound(err) {
					reporter.Partial("target Pod was deleted during capture")
					return
				}
				consecutiveErrors++
				if consecutiveErrors >= 3 {
					reporter.Partial(fmt.Sprintf("target Pod could not be verified after %d attempts: %v", consecutiveErrors, err))
					return
				}
				continue
			}
			consecutiveErrors = 0
			if string(pod.UID) != target.PodUID || pod.Spec.NodeName != target.Node || currentContainerID(pod, target.Container) != target.ContainerID {
				reporter.Partial("target Pod or container was replaced during capture")
				return
			}
		}
	}
}

func currentContainerID(pod *corev1.Pod, container string) string {
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name == container {
			if status.State.Running == nil {
				return ""
			}
			return trimRuntimePrefix(status.ContainerID)
		}
	}
	return ""
}

func snapshotOperator(aspect domain.Aspect, target domain.Target, enricher *visibleEnricher, reporter domain.Reporter, markStarted, markCompleted func()) operators.DataOperator {
	return simple.New("radar-snapshot",
		simple.OnInit(func(gadgetCtx operators.GadgetContext) error {
			found := false
			for _, source := range gadgetCtx.GetDataSources() {
				if source.Type() != datasource.TypeArray {
					continue
				}
				found = true
				formatter, err := igjson.New(source, igjson.WithShowAll(true))
				if err != nil {
					return err
				}
				if err := source.SubscribeArray(func(_ datasource.DataSource, array datasource.DataArray) error {
					raw := append([]byte(nil), formatter.MarshalArray(array)...)
					result := domain.Result{Aspect: aspect, Target: target, CapturedAt: time.Now().UTC(), EventLoss: "unmeasurable"}
					switch aspect {
					case domain.AspectProcesses:
						rows, err := domain.ParseProcesses(raw, target.ContainerID)
						if err != nil {
							return err
						}
						result.Processes = rows
						result.EventCount = len(rows)
					case domain.AspectConnections:
						rows, err := domain.ParseConnections(raw, target.ContainerID)
						if err != nil {
							return err
						}
						result.Connections = enricher.connections(rows)
						result.EventCount = len(rows)
					}
					reporter.Result(result)
					markCompleted()
					gadgetCtx.Cancel()
					return nil
				}, operatorPriority); err != nil {
					return err
				}
			}
			if !found {
				return errors.New("snapshot gadget did not expose an array data source")
			}
			return nil
		}),
		simple.OnStart(func(operators.GadgetContext) error {
			markStarted()
			reporter.Started(target)
			return nil
		}),
	)
}

func dnsOperator(target domain.Target, duration time.Duration, enricher *visibleEnricher, reporter domain.Reporter, markStarted, markCompleted func()) operators.DataOperator {
	aggregator := domain.NewDNSAggregator(target.ContainerID)
	var mu sync.Mutex
	startedAt := time.Time{}
	return simple.New("radar-dns",
		simple.OnInit(func(gadgetCtx operators.GadgetContext) error {
			found := false
			for _, source := range gadgetCtx.GetDataSources() {
				if source.Type() != datasource.TypeSingle {
					continue
				}
				found = true
				formatter, err := igjson.New(source, igjson.WithShowAll(true))
				if err != nil {
					return err
				}
				if err := source.Subscribe(func(_ datasource.DataSource, data datasource.Data) error {
					raw := append(json.RawMessage(nil), formatter.Marshal(data)...)
					mu.Lock()
					accepted, err := aggregator.Add(raw)
					mu.Unlock()
					if accepted {
						reporter.Event(raw)
					}
					return err
				}, operatorPriority); err != nil {
					return err
				}
			}
			if !found {
				return errors.New("DNS gadget did not expose an event data source")
			}
			return nil
		}),
		simple.OnStart(func(operators.GadgetContext) error {
			startedAt = time.Now().UTC()
			markStarted()
			reporter.Started(target)
			return nil
		}),
		simple.OnPostStop(func(operators.GadgetContext) error {
			mu.Lock()
			groups, unmatched, events, truncated := aggregator.Result()
			groups = enricher.dns(groups, target.Namespace)
			mu.Unlock()
			captureDuration := duration
			if !startedAt.IsZero() {
				captureDuration = time.Since(startedAt)
			}
			reporter.Result(domain.Result{
				Aspect: domain.AspectDNS, Target: target, CapturedAt: time.Now().UTC(), CaptureDuration: captureDuration,
				EventCount: events, Truncated: truncated, EventLoss: "unmeasurable", UnmatchedQueries: unmatched, DNS: groups,
			})
			markCompleted()
			return nil
		}),
	)
}

type visibleEnricher struct {
	services []corev1.Service
	pods     []corev1.Pod
}

func loadVisibleEnricher(ctx context.Context, client kubernetes.Interface, target domain.Target) *visibleEnricher {
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result := &visibleEnricher{}
	services, err := client.CoreV1().Services("").List(lookupCtx, metav1.ListOptions{})
	if err != nil {
		services, _ = client.CoreV1().Services(target.Namespace).List(lookupCtx, metav1.ListOptions{})
	}
	if services != nil {
		result.services = services.Items
	}
	pods, err := client.CoreV1().Pods("").List(lookupCtx, metav1.ListOptions{})
	if err != nil {
		pods, _ = client.CoreV1().Pods(target.Namespace).List(lookupCtx, metav1.ListOptions{})
	}
	if pods != nil {
		result.pods = pods.Items
	}
	return result
}

func (e *visibleEnricher) connections(connections []domain.Connection) []domain.Connection {
	if e == nil {
		return connections
	}
	for index := range connections {
		for _, endpoint := range []*domain.Endpoint{&connections[index].Local, &connections[index].Remote} {
			if endpoint.Resource != nil && !e.visible(endpoint.Resource) {
				endpoint.Resource = nil
			}
			if endpoint.Resource != nil || endpoint.Address == "" {
				continue
			}
			for index := range e.services {
				service := &e.services[index]
				if containsString(service.Spec.ClusterIPs, endpoint.Address) {
					endpoint.Resource = &domain.EndpointRef{Kind: "svc", Namespace: service.Namespace, Name: service.Name}
					break
				}
			}
			if endpoint.Resource != nil {
				continue
			}
			for index := range e.pods {
				pod := &e.pods[index]
				if podHasIP(pod, endpoint.Address) {
					endpoint.Resource = &domain.EndpointRef{Kind: "pod", Namespace: pod.Namespace, Name: pod.Name}
					break
				}
			}
		}
	}
	return connections
}

func (e *visibleEnricher) dns(groups []domain.DNSGroup, targetNamespace string) []domain.DNSGroup {
	if e == nil {
		return groups
	}
	for index := range groups {
		query := strings.TrimSuffix(strings.ToLower(groups[index].Name), ".")
		for serviceIndex := range e.services {
			service := &e.services[serviceIndex]
			fullPrefix := strings.ToLower(service.Name + "." + service.Namespace + ".svc")
			nameMatch := query == strings.ToLower(service.Name) && service.Namespace == targetNamespace ||
				query == strings.ToLower(service.Name+"."+service.Namespace) ||
				query == fullPrefix || strings.HasPrefix(query, fullPrefix+".")
			addressMatch := false
			for _, address := range groups[index].Addresses {
				if containsString(service.Spec.ClusterIPs, address) {
					addressMatch = true
					break
				}
			}
			if nameMatch || addressMatch {
				groups[index].Service = service.Namespace + "/" + service.Name
				break
			}
		}
	}
	return groups
}

func (e *visibleEnricher) visible(ref *domain.EndpointRef) bool {
	switch strings.ToLower(ref.Kind) {
	case "svc", "service":
		for index := range e.services {
			service := &e.services[index]
			if service.Namespace == ref.Namespace && service.Name == ref.Name {
				return true
			}
		}
	case "pod":
		for index := range e.pods {
			pod := &e.pods[index]
			if pod.Namespace == ref.Namespace && pod.Name == ref.Name {
				return true
			}
		}
	}
	return false
}

func podHasIP(pod *corev1.Pod, address string) bool {
	for _, podIP := range pod.Status.PodIPs {
		if podIP.IP == address {
			return true
		}
	}
	return pod.Status.PodIP == address
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
