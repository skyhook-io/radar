package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/skyhook-io/radar/internal/substrate/ateapi"
)

// ScopedAtespaces are the atespaces the atespace-only persona may read.
var ScopedAtespaces = []string{"code-review", "support-bots"}

const (
	defaultPageSize = 100
	maxPageSize     = 1000
)

// principal is who a bearer token speaks for, in the two shapes upstream's
// authorization model (model.fga) allows a person: a global viewer, who reads
// everything, or a viewer of named atespaces, who reads actors, templates,
// tags and egress policies there and nothing global. Global viewer is the
// only grant behind ListAtespaces, ListWorkers and the cross-atespace lists,
// and an atespace grant cannot subtract one atespace from it.
type principal struct {
	id        string
	global    bool
	atespaces map[string]bool
}

func (p *principal) canRead(atespace string) bool { return p.global || p.atespaces[atespace] }

type principalKey struct{}

// Faults are read per call so `substrate-demo.sh fault ...` takes effect
// without a restart. Flags set the starting values.
type Faults struct {
	Latency     time.Duration
	Unavailable bool
	Unauth      bool
	File        string
}

func (f Faults) current() Faults {
	if f.File == "" {
		return f
	}
	b, err := os.ReadFile(f.File)
	if err != nil {
		return f
	}
	out := f
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "unavailable":
			out.Unavailable = true
		case line == "unauthenticated":
			out.Unauth = true
		case strings.HasPrefix(line, "latency="):
			if d, err := time.ParseDuration(strings.TrimPrefix(line, "latency=")); err == nil {
				out.Latency = d
			}
		}
	}
	return out
}

type Server struct {
	ateapi.UnimplementedControlServer
	d      *Dataset
	tokens map[string]*principal
	faults Faults
	calls  atomic.Int64

	actorsByKey  map[string]*ateapi.Actor
	actorsBySpc  map[string][]*ateapi.Actor
	tmplByKey    map[string]*ateapi.ActorTemplate
	tmplBySpc    map[string][]*ateapi.ActorTemplate
	tagsByKey    map[string]*ateapi.Tag
	tagsBySpc    map[string][]*ateapi.Tag
	workerByName map[string]*ateapi.Worker
	atespaceByNm map[string]*ateapi.Atespace
}

func NewServer(d *Dataset, globalToken, limitedToken string, faults Faults) *Server {
	s := &Server{
		d: d, faults: faults,
		tokens: map[string]*principal{
			globalToken:  {id: "system:serviceaccount:ate-system:ate-client", global: true},
			limitedToken: {id: "demo-atespace-viewer", atespaces: map[string]bool{ScopedAtespaces[0]: true, ScopedAtespaces[1]: true}},
		},
		actorsByKey: map[string]*ateapi.Actor{}, actorsBySpc: map[string][]*ateapi.Actor{},
		tmplByKey: map[string]*ateapi.ActorTemplate{}, tmplBySpc: map[string][]*ateapi.ActorTemplate{},
		tagsByKey: map[string]*ateapi.Tag{}, tagsBySpc: map[string][]*ateapi.Tag{},
		workerByName: map[string]*ateapi.Worker{}, atespaceByNm: map[string]*ateapi.Atespace{},
	}
	for _, a := range d.Actors {
		k := a.Metadata.Atespace + "/" + a.Metadata.Name
		s.actorsByKey[k] = a
		s.actorsBySpc[a.Metadata.Atespace] = append(s.actorsBySpc[a.Metadata.Atespace], a)
	}
	for _, t := range d.Templates {
		s.tmplByKey[t.Metadata.Atespace+"/"+t.Metadata.Name] = t
		s.tmplBySpc[t.Metadata.Atespace] = append(s.tmplBySpc[t.Metadata.Atespace], t)
	}
	for _, t := range d.Tags {
		s.tagsByKey[t.Metadata.Atespace+"/"+t.Metadata.Name] = t
		s.tagsBySpc[t.Metadata.Atespace] = append(s.tagsBySpc[t.Metadata.Atespace], t)
	}
	for _, w := range d.Workers {
		s.workerByName[w.Metadata.Name] = w
	}
	for _, a := range d.Atespaces {
		s.atespaceByNm[a.Metadata.Name] = a
	}
	return s
}

func (s *Server) gate(ctx context.Context, method string) (context.Context, error) {
	s.calls.Add(1)
	f := s.faults.current()
	if f.Latency > 0 {
		select {
		case <-time.After(f.Latency):
		case <-ctx.Done():
			return nil, status.FromContextError(ctx.Err()).Err()
		}
	}
	if f.Unavailable {
		return nil, status.Error(codes.Unavailable, "fake ate-api-server: injected unavailability")
	}
	md, _ := metadata.FromIncomingContext(ctx)
	var bearer string
	for _, v := range md.Get("authorization") {
		if after, ok := strings.CutPrefix(v, "Bearer "); ok {
			bearer = strings.TrimSpace(after)
		}
	}
	if bearer == "" {
		return nil, status.Error(codes.Unauthenticated, "missing bearer token")
	}
	p, ok := s.tokens[bearer]
	if !ok || f.Unauth {
		return nil, status.Error(codes.Unauthenticated, "invalid bearer token")
	}
	return context.WithValue(ctx, principalKey{}, p), nil
}

// Interceptors applies faults and authentication to every call, then logs
// the method and resulting code. Tokens never reach the log.
func (s *Server) Interceptors() []grpc.ServerOption {
	logCall := func(method string, start time.Time, err error) {
		log.Printf("%s %s %s", strings.TrimPrefix(method, "/substrate.ateapi.v1.Control/"), status.Code(err), time.Since(start).Round(time.Millisecond))
	}
	return []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
			start := time.Now()
			ctx, err := s.gate(ctx, info.FullMethod)
			if err != nil {
				logCall(info.FullMethod, start, err)
				return nil, err
			}
			resp, err := h(ctx, req)
			logCall(info.FullMethod, start, err)
			return resp, err
		}),
		grpc.ChainStreamInterceptor(func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, h grpc.StreamHandler) error {
			if _, err := s.gate(ss.Context(), info.FullMethod); err != nil {
				return err
			}
			return h(srv, ss)
		}),
	}
}

func caller(ctx context.Context) *principal {
	p, _ := ctx.Value(principalKey{}).(*principal)
	return p
}

func denied(p *principal, relation, object string) error {
	return status.Errorf(codes.PermissionDenied, "permission denied: principal %q lacks %q on %q", "user:"+p.id, relation, object)
}

// checkList authorizes a list scoped to atespace ("" = across all).
func checkList(ctx context.Context, relation, atespace string) error {
	p := caller(ctx)
	if atespace == "" {
		if !p.global {
			return denied(p, relation, "global:root")
		}
		return nil
	}
	if !p.canRead(atespace) {
		return denied(p, relation, "atespace:"+atespace)
	}
	return nil
}

func checkObject(ctx context.Context, relation, kind, atespace, name string) error {
	p := caller(ctx)
	if !p.canRead(atespace) {
		return denied(p, relation, kind+":"+atespace+"/"+name)
	}
	return nil
}

func requireRef(ref *ateapi.ObjectRef, field string, atespaced bool) error {
	if ref.GetName() == "" {
		return status.Errorf(codes.InvalidArgument, "%s.name: Required value", field)
	}
	if atespaced && ref.GetAtespace() == "" {
		return status.Errorf(codes.InvalidArgument, "%s.atespace: Required value", field)
	}
	return nil
}

// paginate serves one page. The token binds the list it came from, so a
// token replayed against another list (or atespace) is rejected.
func paginate[T any](items []T, opts *ateapi.ListOptions, scope string) ([]T, string, error) {
	size := int(opts.GetPageSize())
	if size < 0 {
		return nil, "", status.Error(codes.InvalidArgument, "options.page_size: Invalid value: must be non-negative")
	}
	if size == 0 {
		size = defaultPageSize
	}
	if size > maxPageSize {
		size = maxPageSize
	}
	off := 0
	if tok := opts.GetPageToken(); tok != "" {
		raw, err := base64.RawURLEncoding.DecodeString(tok)
		parts := strings.SplitN(string(raw), "|", 3)
		if err != nil || len(parts) != 3 || parts[0] != "v1" || parts[1] != scope {
			return nil, "", status.Error(codes.InvalidArgument, "invalid page token")
		}
		off, err = strconv.Atoi(parts[2])
		if err != nil || off < 0 || off > len(items) {
			return nil, "", status.Error(codes.InvalidArgument, "invalid page token")
		}
	}
	end := min(off+size, len(items))
	next := ""
	if end < len(items) {
		next = base64.RawURLEncoding.EncodeToString(fmt.Appendf(nil, "v1|%s|%d", scope, end))
	}
	return items[off:end], next, nil
}

// requireGlobal gates the RPCs only a global viewer may call.
func requireGlobal(ctx context.Context, relation, object string) error {
	if p := caller(ctx); !p.global {
		return denied(p, relation, object)
	}
	return nil
}

func (s *Server) ListAtespaces(ctx context.Context, req *ateapi.ListAtespacesRequest) (*ateapi.ListAtespacesResponse, error) {
	if err := requireGlobal(ctx, "can_list_atespaces", "global:root"); err != nil {
		return nil, err
	}
	page, next, err := paginate(s.d.Atespaces, req.GetOptions(), "atespaces")
	if err != nil {
		return nil, err
	}
	return &ateapi.ListAtespacesResponse{Atespaces: page, NextPageToken: next}, nil
}

func (s *Server) GetAtespace(ctx context.Context, req *ateapi.GetAtespaceRequest) (*ateapi.Atespace, error) {
	if err := requireRef(req.GetAtespace(), "atespace", false); err != nil {
		return nil, err
	}
	if p := caller(ctx); !p.canRead(req.GetAtespace().GetName()) {
		return nil, denied(p, "can_get", "atespace:"+req.GetAtespace().GetName())
	}
	a, ok := s.atespaceByNm[req.GetAtespace().GetName()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "Atespace %s not found", req.GetAtespace().GetName())
	}
	return a, nil
}

func (s *Server) ListActors(ctx context.Context, req *ateapi.ListActorsRequest) (*ateapi.ListActorsResponse, error) {
	if err := checkList(ctx, "can_list_actors", req.GetAtespace()); err != nil {
		return nil, err
	}
	items := s.d.Actors
	if req.GetAtespace() != "" {
		items = s.actorsBySpc[req.GetAtespace()]
	}
	page, next, err := paginate(items, req.GetOptions(), "actors/"+req.GetAtespace())
	if err != nil {
		return nil, err
	}
	return &ateapi.ListActorsResponse{Actors: page, NextPageToken: next}, nil
}

func (s *Server) GetActor(ctx context.Context, req *ateapi.GetActorRequest) (*ateapi.Actor, error) {
	ref := req.GetActor()
	if err := requireRef(ref, "actor", true); err != nil {
		return nil, err
	}
	if err := checkObject(ctx, "can_get", "actor", ref.GetAtespace(), ref.GetName()); err != nil {
		return nil, err
	}
	a, ok := s.actorsByKey[ref.GetAtespace()+"/"+ref.GetName()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "Actor %s/%s not found", ref.GetAtespace(), ref.GetName())
	}
	return a, nil
}

func (s *Server) GetActorEgressPolicy(ctx context.Context, req *ateapi.GetActorEgressPolicyRequest) (*ateapi.EgressPolicy, error) {
	ref := req.GetActor()
	if err := requireRef(ref, "actor", true); err != nil {
		return nil, err
	}
	if req.GetName() != "default" {
		return nil, status.Errorf(codes.InvalidArgument, "name: Invalid value: %q: must be \"default\"", req.GetName())
	}
	if err := checkObject(ctx, "can_get_egress_policy", "actor", ref.GetAtespace(), ref.GetName()); err != nil {
		return nil, err
	}
	key := ref.GetAtespace() + "/" + ref.GetName()
	if _, ok := s.actorsByKey[key]; !ok {
		return nil, status.Errorf(codes.NotFound, "Actor %s not found", key)
	}
	p, ok := s.d.Egress[key]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "EgressPolicy %s/default not found", key)
	}
	return p, nil
}

func (s *Server) ListActorTemplates(ctx context.Context, req *ateapi.ListActorTemplatesRequest) (*ateapi.ListActorTemplatesResponse, error) {
	if err := checkList(ctx, "can_list_actor_templates", req.GetAtespace()); err != nil {
		return nil, err
	}
	items := s.d.Templates
	if req.GetAtespace() != "" {
		items = s.tmplBySpc[req.GetAtespace()]
	}
	page, next, err := paginate(items, req.GetOptions(), "templates/"+req.GetAtespace())
	if err != nil {
		return nil, err
	}
	return &ateapi.ListActorTemplatesResponse{ActorTemplates: page, NextPageToken: next}, nil
}

func (s *Server) GetActorTemplate(ctx context.Context, req *ateapi.GetActorTemplateRequest) (*ateapi.ActorTemplate, error) {
	ref := req.GetActorTemplate()
	if err := requireRef(ref, "actor_template", true); err != nil {
		return nil, err
	}
	if err := checkObject(ctx, "can_get", "actor_template", ref.GetAtespace(), ref.GetName()); err != nil {
		return nil, err
	}
	t, ok := s.tmplByKey[ref.GetAtespace()+"/"+ref.GetName()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "ActorTemplate %s/%s not found", ref.GetAtespace(), ref.GetName())
	}
	return t, nil
}

func (s *Server) ListTags(ctx context.Context, req *ateapi.ListTagsRequest) (*ateapi.ListTagsResponse, error) {
	if err := checkList(ctx, "can_list_tags", req.GetAtespace()); err != nil {
		return nil, err
	}
	items := s.d.Tags
	if req.GetAtespace() != "" {
		items = s.tagsBySpc[req.GetAtespace()]
	}
	page, next, err := paginate(items, req.GetOptions(), "tags/"+req.GetAtespace())
	if err != nil {
		return nil, err
	}
	return &ateapi.ListTagsResponse{Tags: page, NextPageToken: next}, nil
}

func (s *Server) GetTag(ctx context.Context, req *ateapi.GetTagRequest) (*ateapi.Tag, error) {
	ref := req.GetTag()
	if err := requireRef(ref, "tag", true); err != nil {
		return nil, err
	}
	if err := checkObject(ctx, "can_get", "tag", ref.GetAtespace(), ref.GetName()); err != nil {
		return nil, err
	}
	t, ok := s.tagsByKey[ref.GetAtespace()+"/"+ref.GetName()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "Tag %s/%s not found", ref.GetAtespace(), ref.GetName())
	}
	return t, nil
}

func (s *Server) ListWorkers(ctx context.Context, req *ateapi.ListWorkersRequest) (*ateapi.ListWorkersResponse, error) {
	if err := requireGlobal(ctx, "can_list_workers", "global:root"); err != nil {
		return nil, err
	}
	page, next, err := paginate(s.d.Workers, req.GetOptions(), "workers")
	if err != nil {
		return nil, err
	}
	return &ateapi.ListWorkersResponse{Workers: page, NextPageToken: next}, nil
}

func (s *Server) GetWorker(ctx context.Context, req *ateapi.GetWorkerRequest) (*ateapi.Worker, error) {
	if err := requireRef(req.GetWorker(), "worker", false); err != nil {
		return nil, err
	}
	if err := requireGlobal(ctx, "can_get", "worker:"+req.GetWorker().GetName()); err != nil {
		return nil, err
	}
	w, ok := s.workerByName[req.GetWorker().GetName()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "Worker %s not found", req.GetWorker().GetName())
	}
	return w, nil
}

func (s *Server) ListWorkerActorAssignments(ctx context.Context, req *ateapi.ListWorkerActorAssignmentsRequest) (*ateapi.ListWorkerActorAssignmentsResponse, error) {
	if err := requireRef(req.GetWorker(), "worker", false); err != nil {
		return nil, err
	}
	name := req.GetWorker().GetName()
	if err := requireGlobal(ctx, "can_list_actor_assignments", "worker:"+name); err != nil {
		return nil, err
	}
	if _, ok := s.workerByName[name]; !ok {
		return nil, status.Errorf(codes.NotFound, "Worker %s not found", name)
	}
	page, next, err := paginate(s.d.Assignments[name], req.GetOptions(), "assignments/"+name)
	if err != nil {
		return nil, err
	}
	return &ateapi.ListWorkerActorAssignmentsResponse{ActorAssignments: page, NextPageToken: next}, nil
}
