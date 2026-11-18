// Command fakeapi is a stand-in for Agent Substrate's ate-api-server: the read
// RPCs of substrate.ateapi.v1.Control over TLS with bearer-token auth, serving
// a fixed, internally consistent dataset bound to the worker Pods the
// substrate-demo fixture runs. See scripts/substrate-demo/README.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"maps"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"

	"github.com/skyhook-io/radar/internal/substrate/ateapi"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = cmdServe(os.Args[2:])
	case "manifests":
		err = cmdManifests(os.Args[2:])
	case "pool-status":
		fs := flag.NewFlagSet("pool-status", flag.ExitOnError)
		fs.StringVar(&fixtureNamespace, "namespace", fixtureNamespace, "namespace of the WorkerPools and worker Pods")
		_ = fs.Parse(os.Args[2:])
		for name, patch := range PoolStatusPatches(BuildDataset(time.Now(), nil)) {
			fmt.Printf("%s\t%s\n", name, patch)
		}
	case "probe":
		err = cmdProbe(os.Args[2:])
	case "scenarios":
		err = cmdScenarios(os.Args[2:])
	case "crashlogs":
		err = cmdCrashLogs(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakeapi:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: fakeapi serve|manifests|pool-status|probe|scenarios [flags]")
	os.Exit(2)
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	fs.StringVar(&fixtureNamespace, "namespace", fixtureNamespace, "namespace of the WorkerPools and worker Pods")
	stateDir := fs.String("state", "", "state directory (CA, server cert, tokens, fault file)")
	addr := fs.String("addr", "127.0.0.1:18443", "listen address")
	pods := fs.String("pods", "", "`kubectl get pods -o json` output for the fixture namespace")
	latency := fs.Duration("latency", 0, "delay every call by this much")
	unavailable := fs.Bool("unavailable", false, "fail every call with UNAVAILABLE")
	_ = fs.Parse(args)
	if *stateDir == "" {
		return errors.New("--state is required")
	}
	st, err := LoadOrCreateState(*stateDir)
	if err != nil {
		return err
	}
	podMap, err := ReadPods(*pods)
	if err != nil {
		return err
	}
	srv, lis, err := Start(st, *addr, podMap, Faults{Latency: *latency, Unavailable: *unavailable, File: st.FaultFile})
	if err != nil {
		return err
	}
	_ = os.WriteFile(filepath.Join(st.Dir, "addr"), []byte(lis.Addr().String()+"\n"), 0o644)
	log.Printf("fake ate-api-server listening on %s (serverName %s)", lis.Addr(), ServerName)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	srv.GracefulStop()
	return nil
}

// Start serves the dataset on addr until the returned server stops.
func Start(st *State, addr string, pods map[string]PodInfo, faults Faults) (*grpc.Server, net.Listener, error) {
	anchor := ReadCrashAnchor(st.Dir, pods)
	tlsCfg, err := ServerTLS(st)
	if err != nil {
		return nil, nil, err
	}
	impl := NewServer(func(now time.Time) *Dataset { return buildDataset(now, pods, anchor) }, st.GlobalToken, st.LimitedToken, faults)
	opts := append([]grpc.ServerOption{grpc.Creds(credentials.NewTLS(tlsCfg))}, impl.Interceptors()...)
	srv := grpc.NewServer(opts...)
	ateapi.RegisterControlServer(srv, impl)
	reflection.Register(srv)
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, err
	}
	go func() { _ = srv.Serve(lis) }()
	return srv, lis, nil
}

func cmdManifests(args []string) error {
	fs := flag.NewFlagSet("manifests", flag.ExitOnError)
	fs.StringVar(&fixtureNamespace, "namespace", fixtureNamespace, "namespace of the WorkerPools and worker Pods")
	pods := fs.String("pods", "", "optional pods JSON")
	hashes := fs.Bool("hashes", false, "print `<pod> <script-hash>` per fixture Pod instead")
	_ = fs.Parse(args)
	podMap, err := ReadPods(*pods)
	if err != nil {
		return err
	}
	d := BuildDataset(time.Now(), podMap)
	if *hashes {
		for _, p := range d.plans {
			if p.owned {
				fmt.Printf("%s %s\n", p.pod, podHash(p))
			}
		}
		return nil
	}
	y, err := Manifests(d)
	if err != nil {
		return err
	}
	fmt.Print(y)
	return nil
}

type clientFlags struct {
	addr, ca, serverName, tokenFile string
}

func (c *clientFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&c.addr, "addr", "127.0.0.1:18443", "server address")
	fs.StringVar(&c.ca, "ca", "", "CA bundle (PEM)")
	fs.StringVar(&c.serverName, "server-name", ServerName, "TLS server name")
	fs.StringVar(&c.tokenFile, "token-file", "", "bearer token file")
}

func (c *clientFlags) dial() (ateapi.ControlClient, func(), context.Context, error) {
	tlsCfg, err := ClientTLS(c.ca, c.serverName)
	if err != nil {
		return nil, nil, nil, err
	}
	tok, err := os.ReadFile(c.tokenFile)
	if err != nil {
		return nil, nil, nil, err
	}
	conn, err := grpc.NewClient(c.addr, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	if err != nil {
		return nil, nil, nil, err
	}
	ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+string(trimNL(tok)))
	return ateapi.NewControlClient(conn), func() { conn.Close() }, ctx, nil
}

// ListAllActors pages through ListActors for one atespace ("" = all).
func ListAllActors(ctx context.Context, c ateapi.ControlClient, atespace string, pageSize int32) ([]*ateapi.Actor, int, error) {
	var out []*ateapi.Actor
	token, pages := "", 0
	for {
		resp, err := c.ListActors(ctx, &ateapi.ListActorsRequest{Atespace: atespace, Options: &ateapi.ListOptions{PageSize: pageSize, PageToken: token}})
		if err != nil {
			return out, pages, err
		}
		pages++
		out = append(out, resp.Actors...)
		if token = resp.NextPageToken; token == "" {
			return out, pages, nil
		}
	}
}

func cmdProbe(args []string) error {
	fs := flag.NewFlagSet("probe", flag.ExitOnError)
	var cf clientFlags
	cf.register(fs)
	pageSize := fs.Int("page-size", 1000, "ListActors page size")
	scoped := fs.String("atespaces", strings.Join(ScopedAtespaces, ","), "atespaces to read one by one when cross-atespace lists are denied")
	_ = fs.Parse(args)
	c, closeFn, ctx, err := cf.dial()
	if err != nil {
		return err
	}
	defer closeFn()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	actors, pages, err := ListAllActors(ctx, c, "", int32(*pageSize))
	var deniedSpaces []string
	if status.Code(err) == codes.PermissionDenied {
		fmt.Printf("cross-atespace ListActors: PermissionDenied (%s)\n", status.Convert(err).Message())
		var names []string
		if spaces, lerr := c.ListAtespaces(ctx, &ateapi.ListAtespacesRequest{}); lerr == nil {
			for _, a := range spaces.Atespaces {
				names = append(names, a.Metadata.Name)
			}
		} else if status.Code(lerr) == codes.PermissionDenied {
			// An atespace-only identity cannot discover atespaces; the client
			// has to be told which to read.
			fmt.Printf("ListAtespaces: PermissionDenied; reading the configured atespaces %s\n", *scoped)
			names = strings.Split(*scoped, ",")
		} else {
			return lerr
		}
		actors, pages = nil, 0
		for _, name := range names {
			got, n, aerr := ListAllActors(ctx, c, name, int32(*pageSize))
			if status.Code(aerr) == codes.PermissionDenied {
				deniedSpaces = append(deniedSpaces, name)
				continue
			}
			if aerr != nil {
				return aerr
			}
			actors, pages = append(actors, got...), pages+n
		}
	} else if err != nil {
		return err
	}
	byState := map[string]int{}
	for _, a := range actors {
		byState[strings.TrimPrefix(a.GetStatus().GetState().String(), "ACTOR_STATE_")]++
	}
	fmt.Printf("actors: %d in %d page(s) %v\n", len(actors), pages, sortedCounts(byState))
	if len(deniedSpaces) > 0 {
		fmt.Printf("denied atespaces: %s\n", strings.Join(deniedSpaces, ", "))
	}
	ws, err := c.ListWorkers(ctx, &ateapi.ListWorkersRequest{})
	switch {
	case status.Code(err) == codes.PermissionDenied:
		fmt.Println("workers: PermissionDenied")
	case err != nil:
		return err
	default:
		fmt.Printf("workers: %d\n", len(ws.Workers))
	}
	return nil
}

func sortedCounts(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, m[k]))
	}
	return strings.Join(parts, " ")
}

// cmdCrashLogs prints, once per set of Pods, "<pod>\t<line>" for the lines a
// worker Pod's log holds about the actors that crashed on it, and records the
// crash times they state. It prints nothing when they were written already,
// no crash happened on a running Pod, or the Pods are too young to hold every
// recent crash yet (it says how long to wait).
func cmdCrashLogs(args []string) error {
	fs := flag.NewFlagSet("crashlogs", flag.ExitOnError)
	fs.StringVar(&fixtureNamespace, "namespace", fixtureNamespace, "namespace of the WorkerPools and worker Pods")
	pods := fs.String("pods", "", "`kubectl get pods -o json` output")
	stateDir := fs.String("state", "", "state directory")
	_ = fs.Parse(args)
	podMap, err := ReadPods(*pods)
	if err != nil {
		return err
	}
	if !ReadCrashAnchor(*stateDir, podMap).IsZero() {
		return nil
	}
	anchor := time.Now()
	d := buildDataset(anchor, podMap, anchor)
	if wait := d.storyWait(anchor); wait > 0 {
		fmt.Fprintf(os.Stderr, "    ! crash lines not written: the worker Pods must be %s older to hold the recent crashes; run restart then\n", (wait + time.Minute).Truncate(time.Minute))
		return nil
	}
	lines := d.CrashLines(anchor)
	if len(lines) == 0 {
		return nil
	}
	if err := writeCrashAnchor(*stateDir, podMap, anchor); err != nil {
		return err
	}
	for _, pod := range slices.Sorted(maps.Keys(lines)) {
		for _, l := range lines[pod] {
			fmt.Printf("%s\t%s\n", pod, l)
		}
	}
	return nil
}

func cmdScenarios(args []string) error {
	fs := flag.NewFlagSet("scenarios", flag.ExitOnError)
	fs.StringVar(&fixtureNamespace, "namespace", fixtureNamespace, "namespace of the WorkerPools and worker Pods")
	pods := fs.String("pods", "", "optional pods JSON")
	_ = fs.Parse(args)
	podMap, err := ReadPods(*pods)
	if err != nil {
		return err
	}
	d := BuildDataset(time.Now(), podMap)
	for _, w := range d.Workers {
		s := w.Status
		fmt.Printf("worker %-22s %-36s %-11s epochs issued/registered/observed=%d/%d/%d slots %d/%d\n",
			w.WorkerPod, w.Metadata.Name, strings.TrimPrefix(s.State.String(), "WORKER_STATE_"),
			s.IssuedEpoch, s.RegisteredEpoch, s.ObservedEpoch, s.Allocated.GetActors(), s.Capacity.GetActors())
	}
	for _, a := range d.Actors {
		st := a.Status.State
		if st == ateapi.ActorState_ACTOR_STATE_SUSPENDED || st == ateapi.ActorState_ACTOR_STATE_RUNNING {
			continue
		}
		msg := a.Status.GetCrash().GetMessage()
		fmt.Printf("actor  %s/%s %s %s %s\n", a.Metadata.Atespace, a.Metadata.Name, strings.TrimPrefix(st.String(), "ACTOR_STATE_"),
			time.Since(a.Metadata.UpdateTime.AsTime()).Round(time.Second), msg)
	}
	return nil
}
