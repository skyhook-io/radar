package igdebug

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/skyhook-io/radar/pkg/auth"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/skyhook-io/radar/internal/k8s"
	domain "github.com/skyhook-io/radar/pkg/igdebug"
)

var (
	ErrQuiescing    = errors.New("cluster connection or subsystems are not ready; retry after recovery")
	ErrNotInstalled = errors.New("Inspektor Gadget is not installed in this cluster")
	ErrUnknown      = errors.New("unable to verify Inspektor Gadget installation")
)

type InstallState string

const (
	StateInstalled    InstallState = "installed"
	StateNotInstalled InstallState = "not-installed"
	StateUnknown      InstallState = "unknown"
)

type Status struct {
	State     InstallState `json:"state"`
	Namespace string       `json:"namespace,omitempty"`
	Version   string       `json:"version,omitempty"`
	Message   string       `json:"message,omitempty"`
}

type Service struct {
	mu                sync.Mutex
	generation        uint64
	manager           *domain.Manager
	registry          string
	sharedClient      func() kubernetes.Interface
	clientFromContext func(context.Context) kubernetes.Interface
	connectionReady   func() bool
}

var (
	defaultMu          sync.Mutex
	defaultService     *Service
	configuredRegistry string
)

func SetGadgetRegistry(registry string) {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	configuredRegistry = strings.TrimSuffix(strings.TrimSpace(registry), "/")
	if defaultService != nil {
		defaultService.registry = configuredRegistry
	}
}

func Default() *Service {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	if defaultService == nil {
		defaultService = NewService(configuredRegistry)
	}
	return defaultService
}

func NewService(registry string) *Service {
	return &Service{
		manager:  domain.NewManager(domain.DefaultLimits()),
		registry: strings.TrimSuffix(strings.TrimSpace(registry), "/"),
		sharedClient: func() kubernetes.Interface {
			if client := k8s.GetClient(); client != nil {
				return client
			}
			return nil
		},
		clientFromContext: k8s.ClientFromContext,
		connectionReady: func() bool {
			return k8s.IsConnected() && !k8s.ContextOperationInProgress()
		},
	}
}

func (s *Service) Status(ctx context.Context) Status {
	status := Status{State: StateUnknown, Message: "Cluster connection is not ready"}
	if client := s.sharedClient(); client != nil {
		status = statusWithClient(ctx, client)
		if status.State != StateUnknown {
			return status
		}
	}
	client := s.clientFromContext(ctx)
	if client == nil {
		return status
	}
	return statusWithClient(ctx, client)
}

func statusWithClient(ctx context.Context, client kubernetes.Interface) Status {
	dsList, err := client.AppsV1().DaemonSets("").List(ctx, metav1.ListOptions{LabelSelector: "k8s-app=gadget"})
	if err != nil {
		return Status{State: StateUnknown, Message: fmt.Sprintf("Unable to verify Inspektor Gadget: %v", err)}
	}
	if len(dsList.Items) == 0 {
		return Status{State: StateNotInstalled}
	}
	ds := chooseDaemonSet(dsList.Items)
	status := Status{State: StateInstalled, Namespace: ds.Namespace}
	if len(ds.Spec.Template.Spec.Containers) > 0 {
		status.Version = imageVersion(ds.Spec.Template.Spec.Containers[0].Image)
	}
	return status
}

func (s *Service) Start(ctx context.Context, request domain.Request) (*domain.Run, error) {
	s.mu.Lock()
	generation := s.generation
	s.mu.Unlock()
	if !s.connectionReady() {
		return nil, ErrQuiescing
	}
	if err := domain.ValidateDuration(request.Aspect, request.Duration); err != nil {
		return nil, err
	}
	statusCtx, cancelStatus := context.WithTimeout(ctx, 5*time.Second)
	status := s.Status(statusCtx)
	cancelStatus()
	s.mu.Lock()
	changed := s.generation != generation || !s.connectionReady()
	s.mu.Unlock()
	if changed {
		return nil, ErrQuiescing
	}
	if status.State != StateInstalled {
		if status.State == StateNotInstalled {
			return nil, ErrNotInstalled
		}
		return nil, fmt.Errorf("%w: %s", ErrUnknown, status.Message)
	}
	config, contextName := k8s.GetConfigSnapshot()
	if user := auth.UserFromContext(ctx); user != nil && config != nil {
		config = auth.ImpersonatedConfig(config, user.Username, user.Groups)
	}
	if request.Owner.Context != contextName {
		return nil, ErrQuiescing
	}
	if config == nil {
		return nil, errors.New("unable to build Kubernetes credentials for the current user")
	}
	executor := &runtimeExecutor{
		config:          config,
		gadgetNamespace: status.Namespace,
		registry:        s.registry,
		startupTimeout:  30 * time.Second,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.connectionReady() || s.generation != generation {
		return nil, ErrQuiescing
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.manager.Start(request, executor)
}

func (s *Service) Get(id string, owner domain.Owner) (*domain.Run, error) {
	return s.manager.Get(id, owner)
}

func (s *Service) WaitData(ctx context.Context, id string, owner domain.Owner) (*domain.Run, error) {
	return s.manager.WaitData(ctx, id, owner)
}

func (s *Service) WaitDone(ctx context.Context, id string, owner domain.Owner) (*domain.Run, error) {
	return s.manager.WaitDone(ctx, id, owner)
}

func (s *Service) Stop(id string, owner domain.Owner) (*domain.Run, error) {
	return s.manager.Stop(id, owner)
}

func (s *Service) Subscribe(id string, owner domain.Owner) (<-chan domain.StreamEvent, func(), error) {
	return s.manager.Subscribe(id, owner)
}

func (s *Service) OnContextSwitch() {
	s.mu.Lock()
	s.generation++
	s.mu.Unlock()
	s.manager.OnContextSwitch()
}

func chooseDaemonSet(items []appsv1.DaemonSet) appsv1.DaemonSet {
	for _, item := range items {
		if item.Name == "gadget" {
			return item
		}
	}
	return items[0]
}

func imageVersion(image string) string {
	if at := strings.IndexByte(image, '@'); at >= 0 {
		return image[at+1:]
	}
	lastSlash := strings.LastIndexByte(image, '/')
	if colon := strings.LastIndexByte(image, ':'); colon > lastSlash {
		return image[colon+1:]
	}
	return ""
}
